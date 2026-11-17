package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"strings"
	"unicode"
)

func modelLabel(value string) bool {
	if len(value) > 1024 || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

// Only the revision-bound record used by dispatch supplies feature states.
// ContextWindow is configured policy, not independently measured capacity.
func modelView(cfg config.Config) map[string]any {
	tiers := map[string]any{}
	for _, tier := range []config.Tier{cfg.Cheap, cfg.Frontier} {
		caps := tier.Capabilities
		if !tier.Configured() || caps == nil || !modelLabel(tier.Model) || !modelLabel(caps.Runtime) || !modelLabel(caps.Protocol) || tier.ContextWindow < 0 || tier.ContextWindow > 2147483647 || caps.Validate(tier.Model, tier.RuntimeRevision, tier.Provider) != nil {
			continue
		}
		features := map[string]string{}
		for _, name := range []string{"chat", "streaming", "tools", "structured_output", "image", "embeddings", "ocr"} {
			state := caps.Features[name]
			if state == "" {
				state = "unknown"
			}
			features[name] = state
		}
		tiers[tier.Name] = map[string]any{
			"model": tier.Model, "runtime_revision": caps.Runtime, "protocol": caps.Protocol,
			"local": caps.Local, "features": features, "configured_context_window_tokens": tier.ContextWindow,
		}
	}
	return map[string]any{"schema": "hypermesh.conductor.models.v1", "tiers": tiers}
}
