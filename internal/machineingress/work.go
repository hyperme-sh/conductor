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

func (c Client) Work(ctx context.Context, token string, target CodingTarget, run *ContinueRun, action, digest, phase string) (time.Time, error) {
	if run == nil || run.Current == nil {
		return time.Time{}, refused
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "request_id": uuid.NewString(), "device_id": target.Device, "assignment_id": target.Assignment, "session_id": target.Session, "workspace": target.Workspace, "run_id": run.ID, "task_id": *run.Current, "generation": run.Generation, "action_id": action, "digest": digest, "phase": phase})
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Origin, "/")+"/api/v1/hypermesh/bots/"+target.Bot+"/machine/continue-work", bytes.NewReader(raw))
	if e != nil {
		return time.Time{}, refused
	}
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
		return time.Time{}, refused
	}
	defer response.Body.Close()
	raw, e = io.ReadAll(io.LimitReader(response.Body, 8193))
	if e != nil || len(raw) > 8192 || response.StatusCode != 200 {
		return time.Time{}, refused
	}
	var ack struct {
		Admitted bool    `json:"admitted"`
		Action   string  `json:"action_id"`
		Digest   string  `json:"digest"`
		Expires  float64 `json:"expires_at"`
	}
	if json.Unmarshal(raw, &ack) != nil || !ack.Admitted || ack.Action != action || ack.Digest != digest || ack.Expires <= float64(time.Now().UnixNano())/1e9 {
		return time.Time{}, refused
	}
	return time.Unix(0, int64(ack.Expires*1e9)), nil
}
