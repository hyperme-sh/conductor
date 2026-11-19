package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

func TestPolicyViewContract(t *testing.T) {
	t.Setenv(config.ProxyTokenEnv, "fixture-token")
	path := filepath.Join(t.TempDir(), "private-rules.json")
	if err := os.WriteFile(path, []byte(`{"schema":"hypermesh.conductor.rules.v0","routing":{"class_tiers":{"code":"cheap"},"escalate_when_peak_tokens_above":{"code":512,"private-key":900}},"cache_prefix":{"note":"private-note"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DefaultClass: "parse", RulesFile: path, ClassTiers: map[string]string{"parse": "frontier"}, Cheap: config.Tier{Name: "cheap", Model: "fixture", BaseURL: "http://private-endpoint", APIKeyEnv: "PRIVATE_CREDENTIAL", Locality: "local", RequiredCapabilities: []string{"tools"}, OutputLimit: 128}}
	p := New(cfg, nil, nil)
	p.EnableRecipeReload("")
	read := func(method, route, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, route, nil)
		req.Header.Set(config.ProxyTokenHeader, token)
		out := httptest.NewRecorder()
		p.serveRuntime(out, req)
		return out
	}
	for _, token := range []string{"", "wrong"} {
		if out := read("GET", "/v1/config/policy", token); out.Code != 401 {
			t.Fatal(out.Code)
		}
	}
	if out := read("POST", "/v1/config/policy", "fixture-token"); out.Code != 405 {
		t.Fatal(out.Code)
	}
	out := read("GET", "/v1/config/policy", "fixture-token")
	if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(out.Code, out.Header())
	}
	for _, secret := range []string{"private-endpoint", "PRIVATE_CREDENTIAL", "private-note", "private-key", path, "fixture-token"} {
		if strings.Contains(out.Body.String(), secret) {
			t.Fatal("private configuration leaked")
		}
	}
	var body struct {
		Schema string
		Tiers  map[string]struct {
			Locality string
			Required []string `json:"required_capabilities"`
		}
		Routing struct {
			Default    string            `json:"default_class"`
			Classes    map[string]string `json:"class_tiers"`
			Thresholds map[string]string `json:"escalate_when_peak_tokens_above"`
		}
	}
	if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Schema != "hypermesh.conductor.effective-policy.v1" || body.Routing.Default != "parse" || body.Routing.Classes["parse"] != "frontier" || body.Routing.Classes["code"] != "cheap" || body.Routing.Classes["review"] != "frontier" || body.Routing.Thresholds["code"] != "512" || len(body.Routing.Thresholds) != 1 {
		t.Fatal(out.Body.String())
	}
	if len(body.Tiers) != 1 || body.Tiers["cheap"].Locality != "local" || len(body.Tiers["cheap"].Required) != 1 {
		t.Fatal(out.Body.String())
	}
	old := read("GET", "/v1/config", "fixture-token")
	for _, field := range []string{"routing", "locality", "required_capabilities", "effective-policy"} {
		if strings.Contains(old.Body.String(), field) {
			t.Fatal("legacy contract changed")
		}
	}
	if err := os.WriteFile(path, []byte(`{"private-broken"`), 0600); err != nil {
		t.Fatal(err)
	}
	failed := read("GET", "/v1/config/policy", "fixture-token")
	if failed.Code != 503 || strings.Contains(failed.Body.String(), "private") {
		t.Fatal(failed.Code, failed.Body.String())
	}
	if read("GET", "/v1/config", "fixture-token").Code != 200 {
		t.Fatal("legacy snapshot now depends on policy read")
	}
}

func TestPolicyViewUsesRuntimeClassFallback(t *testing.T) {
	t.Setenv(config.ProxyTokenEnv, "fixture-token")
	for _, name := range []string{"cheap", "frontier"} {
		cfg := config.Config{}
		tier := config.Tier{Name: name, Model: "fixture", BaseURL: "http://127.0.0.1:1"}
		want := "parse"
		if name == "cheap" {
			cfg.Cheap = tier
		} else {
			cfg.Frontier = tier
			want = "code"
		}
		p := New(cfg, nil, nil)
		p.EnableRecipeReload("")
		req := httptest.NewRequest("GET", "/v1/config/policy", nil)
		req.Header.Set(config.ProxyTokenHeader, "fixture-token")
		out := httptest.NewRecorder()
		p.serveRuntime(out, req)
		var body struct {
			Routing struct {
				Default string `json:"default_class"`
			}
		}
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if out.Code != 200 || body.Routing.Default != want {
			t.Fatal(out.Code, out.Body.String())
		}
	}
}

func TestPolicyViewConditionalAlternatives(t *testing.T) {
	t.Setenv(config.ProxyTokenEnv, "fixture-token")
	for _, limit := range []int{10, 20} {
		cfg := config.Config{
			DefaultClass: "code", RefusalFallbackTier: "cheap",
			Cheap:    config.Tier{Name: "cheap", Model: "alternate", BaseURL: "http://fixture", TaskOutputLimit: limit},
			Frontier: config.Tier{Name: "frontier", Model: "primary", BaseURL: "http://fixture", TaskOutputLimit: 10},
		}
		p := New(cfg, nil, nil)
		p.EnableRecipeReload("")
		for _, optIn := range []bool{false, true} {
			req := httptest.NewRequest("GET", "/v1/config/policy", nil)
			req.Header.Set(config.ProxyTokenHeader, "fixture-token")
			if optIn {
				req.Header.Set("X-Conductor-Policy-View", "conditional-alternatives.v1")
			}
			out := httptest.NewRecorder()
			p.serveRuntime(out, req)
			var body struct {
				Alternatives []map[string]string `json:"conditional_alternatives"`
			}
			if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &body) != nil {
				t.Fatal(out.Code, out.Body.String())
			}
			if !optIn && body.Alternatives != nil {
				t.Fatal("unnegotiated policy changed")
			}
			if optIn && limit == 10 {
				if len(body.Alternatives) != 1 || body.Alternatives[0]["from_tier"] != "frontier" ||
					body.Alternatives[0]["to_tier"] != "cheap" || body.Alternatives[0]["condition"] != "upstream-refusal" {
					t.Fatal("expected the conditional frontier fallback", body.Alternatives)
				}
			} else if len(body.Alternatives) != 0 {
				t.Fatal("reported an ineligible fallback")
			}
		}
	}
}
