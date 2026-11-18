package machineingress

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/agentadvice"
	"github.com/hyperme-sh/hypermesh-conductor/internal/projectplan"
	"io"
	"net/http"
	"strings"
	"time"
)

type PlanInput struct {
	Binding     Binding             `json:"binding"`
	Destination string              `json:"destination"`
	MaxOutput   int                 `json:"max_output_tokens"`
	Plan        projectplan.Request `json:"plan"`
}

func (h *Handler) plan(w http.ResponseWriter, r *http.Request, raw []byte) {
	var in PlanInput
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.Binding.Tenant != h.Tenant || len(in.Destination) > 256 {
		http.Error(w, "Invalid plan", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if _, e := h.Client.Check(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), in.Binding, "", "", "", ""); e != nil {
		http.Error(w, "Plan scope unavailable", 403)
		return
	}
	if in.Plan.Advice != nil {
		a, e := agentadvice.Recommend(*in.Plan.Advice, time.Now())
		if e != nil {
			http.Error(w, "Invalid observations", 400)
			return
		}
		json.NewEncoder(w).Encode(a)
		return
	}
	model, capacity, outputLimit := "unknown", 0, 0
	if h.ModelCapacity != nil {
		model, capacity, outputLimit = h.ModelCapacity(in.Destination)
	}
	p, e := projectplan.Build(in.Plan, in.Binding.Assignment, model, capacity, in.MaxOutput)
	if e != nil {
		http.Error(w, "Invalid bounded plan", 400)
		return
	}
	if outputLimit > 0 && in.MaxOutput > outputLimit {
		p.NeedsInput = append(p.NeedsInput, "configured output allowance exceeded")
		for i := range p.Chunks {
			p.Chunks[i].Ready = false
		}
	}
	json.NewEncoder(w).Encode(p)
}
func Plan(ctx context.Context, socket, credential string, in PlanInput) (json.RawMessage, error) {
	raw, e := json.Marshal(in)
	if e != nil {
		return nil, e
	}
	return privateCall(ctx, socket, credential, raw, "/v1/machine/plan")
}
