package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/routing"
	"github.com/hyperme-sh/hypermesh-conductor/internal/translate"
)

// Preview is opt-in and only models a fresh task before runtime review/rewrites.
// It must never read a task ledger or call the generation/dispatch path.
func (p *Proxy) serveRecipePreview(w http.ResponseWriter, r *http.Request) {
	refuse := func(status int, reason string) { writeJSON(w, status, map[string]any{"error": reason}) }
	if os.Getenv("CONDUCTOR_RECIPE_PREVIEW_ENABLED") != "1" {
		refuse(404, "recipe preview disabled")
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil {
		refuse(413, "preview body unavailable or too large")
		return
	}
	var input struct {
		Recipe json.RawMessage `json:"recipe"`
		Tier   string          `json:"tier"`
		Chat   json.RawMessage `json:"chat"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		refuse(400, "invalid preview request")
		return
	}
	candidate, validation := config.PreviewRecipe(input.Recipe, p.Config)
	if !validation.Valid {
		writeJSON(w, 422, validation)
		return
	}
	var tier config.Tier
	switch input.Tier {
	case routing.Cheap:
		tier = candidate.Cheap
	case routing.Frontier:
		tier = candidate.Frontier
	default:
		refuse(400, "invalid preview tier")
		return
	}
	// Gateway requires task/lease authority; this fresh-task preview cannot create it.
	if !tier.Configured() || tier.Gateway || tier.InputCounter != "vllm" || tier.ContextWindow <= 0 || tier.OutputLimit <= 0 {
		refuse(422, "preview requires a configured non-gateway chat tokenizer and context limit")
		return
	}
	wire, err := translateTierRequest(translate.ProtoChat, tier, input.Chat)
	if err != nil || wire.Protocol != translate.ProtoChat || wire.LeaseID != "" {
		refuse(400, "unsupported preview chat")
		return
	}
	var allowance generationAllowance
	wire.Body, allowance, err = boundedOutput(wire.Protocol, wire.Body, tier.OutputLimit)
	if err != nil {
		refuse(400, "invalid preview output allowance")
		return
	}
	_, upstream, err := p.upstreamRequest(r, tier, wire, false, "")
	if err != nil {
		refuse(401, "preview provider authorization unavailable")
		return
	}
	counted, status, err := p.countChatContextDetails(r, upstream, tier, wire.Protocol, wire.Body, "")
	if err != nil {
		if status == 400 {
			refuse(422, "preview input or context allowance refused")
		} else {
			refuse(503, "preview token count unavailable")
		}
		return
	}
	// Match admission's candidate multiplier, using int64 before multiplication.
	inputCharge := int64(counted.input) * int64(allowance.candidates)
	outputCharge := int64(allowance.output)
	if (tier.TaskTokenLimit > 0 && inputCharge+outputCharge > int64(tier.TaskTokenLimit)) || (tier.TaskOutputLimit > 0 && outputCharge > int64(tier.TaskOutputLimit)) {
		refuse(422, "preview exceeds new-task allowance")
		return
	}
	var remainingTotal, remainingOutput *int64
	if tier.TaskTokenLimit > 0 {
		n := int64(tier.TaskTokenLimit) - inputCharge - outputCharge
		remainingTotal = &n
	}
	if tier.TaskOutputLimit > 0 {
		n := int64(tier.TaskOutputLimit) - outputCharge
		remainingOutput = &n
	}
	capacity := min(tier.ContextWindow, counted.capacity)
	writeJSON(w, 200, map[string]any{
		"schema": "hypermesh.conductor.recipe-preview.v1", "scope": "new-task-before-review", "admitted": false,
		"recipe_sha256": tier.RecipeDigest, "tier": input.Tier,
		"counted_input_tokens": counted.input, "candidates": allowance.candidates,
		"input_reservation_tokens": inputCharge, "output_reservation_tokens": outputCharge,
		"effective_context_window_tokens": capacity,
		"context_headroom_tokens":         capacity - counted.input - allowance.output/allowance.candidates,
		"remaining_task_tokens":           remainingTotal, "remaining_task_output_tokens": remainingOutput,
	})
}
