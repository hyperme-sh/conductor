package machineingress

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"time"
)

type ContinueRun struct {
	Generation int     `json:"worker_generation"`
	ID         string  `json:"id"`
	Revision   int     `json:"revision"`
	Continue   bool    `json:"continue_enabled"`
	State      string  `json:"state"`
	Workspace  string  `json:"workspace"`
	Assignment string  `json:"assignment_id"`
	Current    *string `json:"current"`
	Chunks     []struct {
		ID      string   `json:"id"`
		Depends []string `json:"depends_on"`
		State   string   `json:"state"`
	} `json:"chunks"`
}

// NextChunk selects only declared dependency-ready work. Unknown states never
// imply permission to choose another backlog item or retry a consumed operation.
func NextChunk(run ContinueRun) string {
	if run.State != "running" || !run.Continue || run.Current != nil {
		return ""
	}
	if len(run.Chunks) == 0 || len(run.Chunks) > 16 {
		return ""
	}
	states := map[string]string{}
	for _, c := range run.Chunks {
		if !canonicalID(c.ID) || states[c.ID] != "" {
			return ""
		}
		states[c.ID] = c.State
	}
	for _, c := range run.Chunks {
		if c.State != "pending" {
			continue
		}
		ready := true
		for _, dep := range c.Depends {
			if states[dep] != "passed" {
				ready = false
			}
		}
		if ready {
			return c.ID
		}
	}
	return ""
}

func (c Client) continueCall(ctx context.Context, token string, target CodingTarget, action string, extra map[string]any) (*ContinueRun, error) {
	if !ValidOrigin(c.Origin) || !target.Binding.Valid() || !strings.HasPrefix(token, "hm_msi_") || strings.ContainsAny(token, "\r\n") || len(token) > 4096 {
		return nil, refused
	}
	body := map[string]any{"version": 1, "request_id": uuid.NewString(), "device_id": target.Device, "assignment_id": target.Assignment, "session_id": target.Session, "workspace": target.Workspace}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Origin, "/")+"/api/v1/hypermesh/bots/"+target.Bot+"/machine/continue-"+action, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", target.Tenant)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(req)
	if e != nil {
		return nil, refused
	}
	defer response.Body.Close()
	// Older Plane deployments can still serve explicitly admitted single tasks.
	if response.StatusCode == 404 && action == "state" {
		return nil, nil
	}
	if response.StatusCode != 200 {
		return nil, refused
	}
	raw, e = io.ReadAll(io.LimitReader(response.Body, 131073))
	if e != nil || len(raw) > 131072 {
		return nil, refused
	}
	var result struct {
		Run *ContinueRun `json:"run"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil, refused
	}
	if result.Run != nil && (!canonicalID(result.Run.ID) || result.Run.Revision < 1 || result.Run.Generation < 0 || result.Run.Generation > 17 || result.Run.Workspace != target.Workspace || result.Run.Assignment != target.Assignment || len(result.Run.Chunks) > 16) {
		return nil, refused
	}
	return result.Run, nil
}

// ContinueCoding is one bounded iteration of the durable loop. Visor's existing
// worker invokes the next iteration while keeping its consent heartbeat alive.
// All state and selected-task ownership remain in Plane; process restart cannot
// reset pause, budgets or a consumed operation. There are no execution retries.
func (c Client) ContinueCoding(ctx context.Context, token, socket string, target CodingTarget) (CodingResult, error) {
	return c.continueCoding(ctx, token, target, func(ctx context.Context, expected string, generation int) (CodingResult, error) {
		return c.runSelectedCodingGeneration(ctx, token, target, expected, generation, func(ctx context.Context, raw []byte) (json.RawMessage, error) { return Submit(ctx, socket, token, raw) })
	})
}
func (c Client) continueCoding(ctx context.Context, token string, target CodingTarget, execute func(context.Context, string, int) (CodingResult, error)) (CodingResult, error) {
	run, e := c.continueCall(ctx, token, target, "state", nil)
	if e != nil {
		return CodingResult{}, e
	}
	if run != nil {
		if run.State != "running" || (!run.Continue && run.Current == nil) {
			return CodingResult{Version: 1, State: "idle"}, nil
		}
		if run.Current == nil {
			next := NextChunk(*run)
			if next == "" {
				return CodingResult{Version: 1, State: "idle"}, nil
			}
			run, e = c.continueCall(ctx, token, target, "advance", map[string]any{"run_id": run.ID, "revision": run.Revision, "chunk_id": next})
			if e != nil {
				return CodingResult{}, e
			}
			if run == nil || run.State != "running" || run.Current == nil {
				return CodingResult{Version: 1, State: "idle"}, nil
			}
		}
	}
	expected := ""
	if run != nil && run.Current != nil {
		expected = *run.Current
	}
	generation := 0
	if run != nil {
		generation = run.Generation
	}
	result, e := execute(ctx, expected, generation)
	if e != nil {
		return result, e
	}
	if run != nil && result.State != "idle" {
		if run.Current == nil || result.Task != *run.Current {
			return CodingResult{}, refused
		}
		if result.OutputSHA != "" && result.CheckOutcome != "" {
			_, e = c.continueCall(ctx, token, target, "record", map[string]any{"run_id": run.ID, "chunk_id": result.Task, "outcome": result.CheckOutcome, "output_sha256": result.OutputSHA})
			if e != nil {
				result.State = "unknown"
				result.OutputSHA = ""
			}
		}
	}
	return result, nil
}
