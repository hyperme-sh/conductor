package machineingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
	"time"
)

// CodingTarget is supplied by the trusted host configuration, never prompt text.
type CodingTarget struct {
	Binding
	Workspace   string `json:"workspace"`
	Class       string `json:"class"`
	Destination string `json:"destination"`
	MaxOutput   int    `json:"max_output_tokens"`
}
type CodingResult struct {
	CheckOutcome string `json:"-"`
	Version      int    `json:"version"`
	Task         string `json:"task_id,omitempty"`
	State        string `json:"state"`
	OutputSHA    string `json:"output_sha256,omitempty"`
}

func sameIdentity(a, b Fact) bool {
	return a.Tenant == b.Tenant && a.Owner == b.Owner && a.Principal == b.Principal && a.Organization == b.Organization && a.Device == b.Device && a.Bot == b.Bot && a.Assignment == b.Assignment && a.Session == b.Session
}

// RunCoding consumes at most one already-admitted task. A lost response is never
// retried. Model output is metadata only and cannot schedule additional actions.
func (c Client) RunCoding(ctx context.Context, token, socket string, target CodingTarget) (CodingResult, error) {
	return c.runSelectedCoding(ctx, token, target, "", func(ctx context.Context, raw []byte) (json.RawMessage, error) { return Submit(ctx, socket, token, raw) })
}
func (c Client) runCoding(ctx context.Context, token string, target CodingTarget, submit func(context.Context, []byte) (json.RawMessage, error)) (CodingResult, error) {
	return c.runSelectedCoding(ctx, token, target, "", submit)
}
func (c Client) runSelectedCoding(ctx context.Context, token string, target CodingTarget, expected string, submit func(context.Context, []byte) (json.RawMessage, error)) (CodingResult, error) {
	if len(target.Workspace) < 2 || len(target.Workspace) > 4096 || strings.ContainsAny(target.Workspace, "\x00\r\n") || target.Class == "" || target.Destination == "" || target.MaxOutput < 1 || target.MaxOutput > 32768 {
		return CodingResult{}, refused
	}
	fact, e := c.ActiveContext(ctx, token, target.Binding)
	if e != nil {
		return CodingResult{}, e
	}
	nonce := uuid.NewString()
	claimBody := map[string]any{"version": 1, "request_id": nonce, "device_id": target.Device, "assignment_id": target.Assignment, "session_id": target.Session, "workspace": target.Workspace}
	if expected != "" {
		if !canonicalID(expected) {
			return CodingResult{}, refused
		}
		claimBody["task_id"] = expected
	}
	body, _ := json.Marshal(claimBody)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(callCtx, "POST", strings.TrimRight(c.Origin, "/")+"/api/v1/hypermesh/bots/"+target.Bot+"/machine/coding-claim", bytes.NewReader(body))
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
		return CodingResult{}, refused
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 70001))
	if e != nil || len(raw) > 70000 || response.StatusCode != 200 {
		return CodingResult{}, refused
	}
	var claim struct {
		Request  string `json:"request_id"`
		Identity Fact   `json:"identity"`
		Task     *struct {
			ID         string `json:"id"`
			Assignment string `json:"assignment_id"`
			Session    string `json:"session_id"`
			Capability string `json:"capability"`
			Status     string `json:"status"`
			Payload    struct {
				Version    int    `json:"version"`
				Workspace  string `json:"workspace"`
				Device     string `json:"device_id"`
				Operation  string `json:"operation_id"`
				Digest     string `json:"request_sha256"`
				Submission string `json:"submission_base64"`
			} `json:"payload"`
		} `json:"task"`
	}
	if json.Unmarshal(raw, &claim) != nil || claim.Request != nonce || !sameIdentity(fact, claim.Identity) || claim.Identity.Expires <= float64(time.Now().UnixNano())/1e9 {
		return CodingResult{}, refused
	}
	if claim.Task == nil {
		return CodingResult{Version: 1, State: "idle"}, nil
	}
	t := claim.Task
	p := t.Payload
	if (expected != "" && t.ID != expected) || !canonicalID(t.ID) || t.Assignment != target.Assignment || t.Session != target.Session || t.Capability != "coding" || t.Status != "delivered" || p.Version != 1 || p.Workspace != target.Workspace || p.Device != target.Device {
		return CodingResult{}, refused
	}
	original, e := base64.StdEncoding.DecodeString(p.Submission)
	if e != nil || len(original) > 48000 {
		return CodingResult{}, refused
	}
	sum := sha256.Sum256(original)
	var s Submission
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&s) != nil || decoder.Decode(new(any)) != io.EOF || s.Binding != target.Binding || s.Task != t.ID || s.Operation != p.Operation || hex.EncodeToString(sum[:]) != p.Digest || s.Class != target.Class || s.Destination != target.Destination || s.MaxOutput != target.MaxOutput {
		return CodingResult{}, refused
	}
	// Plane has already committed delivery. Any subsequent failure is uncertain.
	result := CodingResult{Version: 1, Task: t.ID, State: "unknown"}
	output, e := submit(ctx, original)
	if e != nil {
		return result, nil
	}
	var completion struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(output, &completion) != nil || len(completion.Choices) != 1 || len(completion.Choices[0].Message.Content) == 0 || len(completion.Choices[0].Message.Content) > 65536 {
		return result, nil
	}
	digest := sha256.Sum256(output)
	result.OutputSHA = hex.EncodeToString(digest[:])
	result.CheckOutcome = "needs-input"
	if completion.Choices[0].Finish == "stop" {
		result.State = "output-observed"
		result.CheckOutcome = "passed"
	}
	if completion.Choices[0].Finish == "length" {
		result.CheckOutcome = "limit"
	}
	return result, nil
}
