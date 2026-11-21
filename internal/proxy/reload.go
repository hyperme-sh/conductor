package proxy

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
	"github.com/hyperme-sh/hypermesh-conductor/internal/rules"
)

type recipeRuntime struct {
	path   string
	active atomic.Pointer[Proxy]
	mu     sync.Mutex
}

// EnableRecipeReload preserves an immutable configuration snapshot per request.
// Listener, endpoint and policy changes require a process restart.
func (p *Proxy) EnableRecipeReload(path string) {
	child := New(p.Config, p.Hop, p.Journal)
	child.PeerFactory = p.PeerFactory
	child.PeerIngress = p.PeerIngress
	child.PhoneIngress = p.PhoneIngress
	child.PhoneFactory = p.PhoneFactory
	child.RemoteAgentIngress = p.RemoteAgentIngress
	child.RemoteAgentFactory = p.RemoteAgentFactory
	child.MachineIngress = p.MachineIngress
	child.MachineFactory = p.MachineFactory
	child.Notes = p.Notes
	child.Logf = p.Logf
	runtime := &recipeRuntime{path: path}
	runtime.active.Store(child)
	p.recipes = runtime
}

func withoutRecipe(c config.Config) config.Config {
	for _, tier := range []*config.Tier{&c.Cheap, &c.Frontier} {
		tier.RecipeDigest = ""
		tier.RequiredCapabilities = nil
		tier.OutputLimit = 0
		tier.TaskTokenLimit = 0
		tier.TaskOutputLimit = 0
		tier.ContextWindow = 0
	}
	return c
}

func (r *recipeRuntime) reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := config.Load(r.path)
	if err != nil {
		return err
	}
	old := r.active.Load()
	next.Journal = old.Config.Journal
	if !reflect.DeepEqual(withoutRecipe(next), withoutRecipe(old.Config)) {
		return fmt.Errorf("only recipe limits may reload; endpoint or policy changes require restart")
	}
	child := New(next, old.Hop, old.Journal)
	child.PeerFactory = old.PeerFactory
	child.PeerIngress = old.PeerIngress
	child.PhoneIngress = old.PhoneIngress
	child.PhoneFactory = old.PhoneFactory
	if child.PhoneFactory != nil {
		child.PhoneIngress, err = child.PhoneFactory(next)
		if err != nil {
			return err
		}
	}
	child.RemoteAgentIngress = old.RemoteAgentIngress
	child.RemoteAgentFactory = old.RemoteAgentFactory
	if child.RemoteAgentFactory != nil {
		child.RemoteAgentIngress, err = child.RemoteAgentFactory(next)
		if err != nil {
			return err
		}
	}
	child.MachineIngress = old.MachineIngress
	child.MachineFactory = old.MachineFactory
	if child.MachineFactory != nil {
		child.MachineIngress, err = child.MachineFactory(next)
		if err != nil {
			return err
		}
	}
	if child.PeerFactory != nil {
		child.PeerIngress, err = child.PeerFactory(next)
		if err != nil {
			return err
		}
	}
	child.Notes = old.Notes
	child.Logf = old.Logf
	r.active.Store(child)
	return nil
}

func (p *Proxy) serveRuntime(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/config" && r.URL.Path != "/v1/config/reload" && r.URL.Path != "/v1/config/policy" && r.URL.Path != "/v1/config/models" && r.URL.Path != "/v1/config/validate" && r.URL.Path != "/v1/config/preview" {
		p.recipes.active.Load().ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/v1/config/models" || r.URL.Path == "/v1/config/validate" || r.URL.Path == "/v1/config/preview" {
		w.Header().Set("Cache-Control", "no-store")
	}
	if err := authorizeProxy(r); err != nil {
		writeJSON(w, 401, map[string]any{"error": "proxy authorization required"})
		return
	}
	if r.URL.Path == "/v1/config/preview" {
		p.recipes.active.Load().serveRecipePreview(w, r)
		return
	}
	if r.URL.Path == "/v1/config/validate" {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil {
			writeJSON(w, 413, map[string]any{"error": "recipe body unavailable or too large"})
			return
		}
		writeJSON(w, 200, config.ValidateRecipe(body, p.recipes.active.Load().Config))
		return
	}
	if r.URL.Path == "/v1/config/reload" {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if err := p.recipes.reload(); err != nil {
			writeJSON(w, 400, map[string]any{"error": "recipe reload rejected; previous revision retained"})
			return
		}
	} else if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	cfg := p.recipes.active.Load().Config
	if r.URL.Path == "/v1/config/models" {
		writeJSON(w, 200, modelView(cfg))
		return
	}
	policyView := r.URL.Path == "/v1/config/policy"
	if policyView {
		w.Header().Set("Cache-Control", "no-store")
	}
	var policy rules.File
	if policyView {
		var err error
		policy, err = rules.Load(cfg.RulesFile)
		if err != nil {
			writeJSON(w, 503, map[string]any{"error": "routing policy unavailable"})
			return
		}
	}
	result := map[string]any{}
	for _, tier := range []config.Tier{cfg.Cheap, cfg.Frontier} {
		if tier.Configured() {
			result[tier.Name] = map[string]any{"model": tier.Model, "recipe_sha256": tier.RecipeDigest, "max_output_tokens": tier.OutputLimit,
				"max_task_tokens": tier.TaskTokenLimit, "max_task_output_tokens": tier.TaskOutputLimit, "context_window_tokens": tier.ContextWindow}
			if policyView {
				row := result[tier.Name].(map[string]any)
				row["required_capabilities"] = append([]string{}, tier.RequiredCapabilities...)
				row["locality"] = tier.Locality
			}
		}
	}
	if policyView {
		defaultClass := cfg.DefaultClass
		if defaultClass == "" {
			var err error
			defaultClass, err = cfg.FallbackClass()
			if err != nil {
				writeJSON(w, 503, map[string]any{"error": "routing policy unavailable"})
				return
			}
		}
		classes := map[string]string{}
		for class := range routing.DefaultTiers {
			decision, err := routing.Route(class, "", rules.EffectiveClassTiers(cfg.ClassTiers, policy, class, 0))
			if err != nil {
				writeJSON(w, 503, map[string]any{"error": "routing policy unavailable"})
				return
			}
			classes[class] = decision.Tier
		}
		thresholds := map[string]string{}
		for class := range classes {
			if limit, ok := policy.Routing.EscalateWhenPeakTokensAbove[class]; ok {
				thresholds[class] = strconv.Itoa(limit)
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		view := map[string]any{"schema": "hypermesh.conductor.effective-policy.v1", "tiers": result,
			"routing": map[string]any{"default_class": defaultClass, "class_tiers": classes, "escalate_when_peak_tokens_above": thresholds}}
		if r.Header.Get("X-Conductor-Policy-View") == "conditional-alternatives.v1" {
			alternatives := []map[string]string{}
			for _, tier := range []config.Tier{cfg.Cheap, cfg.Frontier} {
				if next, eligible := refusalAlternative(cfg, tier, 1); tier.Configured() && eligible {
					alternatives = append(alternatives, map[string]string{
						"from_tier": tier.Name, "to_tier": next.Name, "condition": "upstream-refusal",
					})
				}
			}
			view["conditional_alternatives"] = alternatives
		}
		writeJSON(w, 200, view)
		return
	}
	writeJSON(w, 200, map[string]any{"schema": "hypermesh.conductor.effective.v1", "tiers": result})
}
