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

func (c Client) reserveMoney(ctx context.Context, token string, s Submission, digest, currency string, amount int64, source string) error {
	attempt := uuid.NewString()
	body := map[string]any{"version": 1, "request_id": uuid.NewString(), "device_id": s.Device, "assignment_id": s.Assignment, "session_id": s.Session, "task_id": s.Task, "operation_id": s.Operation, "request_sha256": digest, "command": "check", "attempt_id": attempt, "currency": currency, "maximum_micros": amount, "price_source": source}
	raw, _ := json.Marshal(body)
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Origin, "/")+"/api/v1/hypermesh/bots/"+s.Bot+"/machine/continue-money", bytes.NewReader(raw))
	if e != nil {
		return refused
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", s.Tenant)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 5 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(req)
	if e != nil {
		return refused
	}
	defer response.Body.Close()
	raw, e = io.ReadAll(io.LimitReader(response.Body, 8193))
	if e != nil || len(raw) > 8192 || response.StatusCode != 200 {
		return refused
	}
	var ack struct {
		Admitted bool   `json:"admitted"`
		Attempt  string `json:"attempt_id"`
		Currency string `json:"currency"`
		Amount   int64  `json:"maximum_micros"`
	}
	if json.Unmarshal(raw, &ack) != nil || !ack.Admitted || ack.Attempt != attempt || ack.Currency != currency || ack.Amount != amount {
		return refused
	}
	return nil
}
