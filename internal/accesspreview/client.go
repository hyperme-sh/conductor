// Package accesspreview verifies identity with Plane and policy with AIuditor.
// Its result is a read-only preview, never execution authority.
package accesspreview

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
)

var Refused = errors.New("permission preview unavailable")

type Limits struct {
	Bytes   uint64 `json:"max_bytes"`
	Seconds uint32 `json:"timeout_seconds"`
}
type Rule struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Limits    Limits `json:"limits"`
}
type Action struct {
	Rule
	Arguments string `json:"arguments_sha256"`
}
type Request struct {
	ExpectedPolicy string  `json:"expected_policy_revision,omitempty"`
	ContextExpires float64 `json:"context_expires_at,omitempty"`
	machineingress.Binding
	RequestID       string `json:"request_id"`
	Project         string `json:"project"`
	TemplateVersion int    `json:"template_version"`
	Template        []Rule `json:"template"`
	Action          Action `json:"action"`
}
type Envelope struct {
	Credential string  `json:"credential"`
	Preview    Request `json:"preview"`
}
type Identity struct {
	Tenant       string `json:"tenant_id"`
	Owner        string `json:"owner_id"`
	Principal    string `json:"principal"`
	Organization string `json:"organization_id"`
	Device       string `json:"device_id"`
	Bot          string `json:"bot_id"`
	Assignment   string `json:"assignment_id"`
	Session      string `json:"session_id"`
}
type Result struct {
	Version        int      `json:"version"`
	RequestID      string   `json:"request_id"`
	PreviewOnly    bool     `json:"preview_only"`
	Identity       Identity `json:"identity"`
	Action         Action   `json:"action"`
	Project        string   `json:"project"`
	Decision       string   `json:"decision"`
	Permitted      bool     `json:"permitted"`
	Reason         string   `json:"reason"`
	Limits         Limits   `json:"limits"`
	PolicyRevision string   `json:"policy_revision"`
	Learning       bool     `json:"learning_eligible"`
	Expires        float64  `json:"expires_at"`
}
type Client struct {
	Plane    machineingress.Client
	Endpoint string
	HTTP     *http.Client
}

func decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return Refused
	}
	return nil
}
func id(value string) bool {
	u, e := uuid.Parse(value)
	return e == nil && u != uuid.Nil && u.String() == value
}
func limit(l Limits) bool {
	return l.Bytes > 0 && l.Bytes <= 67108864 && l.Seconds > 0 && l.Seconds <= 300
}
func (c Client) Preview(ctx context.Context, raw []byte) (Result, error) {
	return c.review(ctx, raw, false)
}

// ReviewRead binds a current active Plane identity to AIuditor's exact read.
func (c Client) ReviewRead(ctx context.Context, raw []byte) (Result, error) {
	return c.review(ctx, raw, true)
}

func (c Client) review(ctx context.Context, raw []byte, active bool) (Result, error) {
	var envelope Envelope
	if len(raw) > 262144 || decode(raw, &envelope) != nil {
		return Result{}, Refused
	}
	p := envelope.Preview
	if !p.Binding.Valid() || !id(p.RequestID) || p.TemplateVersion != 1 || len(p.Template) < 1 || len(p.Template) > 32 || len(p.Project) == 0 || len(p.Project) > 4096 || !limit(p.Action.Limits) || len(p.Action.Arguments) != 64 {
		return Result{}, Refused
	}
	for _, r := range p.Template {
		if !limit(r.Limits) || len(r.Target) == 0 || len(r.Target) > 4096 {
			return Result{}, Refused
		}
	}
	endpointPath := "/api/v1/aiuditor/access/preview"
	if active {
		endpointPath = "/api/v1/aiuditor/access/read-review"
		digest, err := hex.DecodeString(p.Action.Arguments)
		if err != nil || len(digest) != 32 || strings.ToLower(p.Action.Arguments) != p.Action.Arguments || p.Action.Operation != "read" || !path.IsAbs(p.Project) || path.Clean(p.Project) != p.Project || p.Project == "/" || path.Clean(p.Action.Target) != p.Action.Target || !strings.HasPrefix(p.Action.Target, p.Project+"/") || strings.ContainsAny(p.Action.Target, "*?\\") || p.ExpectedPolicy != "" && !id(p.ExpectedPolicy) {
			return Result{}, Refused
		}
		for _, ch := range p.Action.Target {
			if ch < 32 || ch == 127 {
				return Result{}, Refused
			}
		}
	} else if p.ExpectedPolicy != "" {
		return Result{}, Refused
	}
	endpoint, e := url.Parse(c.Endpoint)
	if e != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != endpointPath {
		return Result{}, Refused
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var fact machineingress.Fact
	if active {
		fact, e = c.Plane.ActiveContext(ctx, envelope.Credential, p.Binding)
	} else {
		fact, e = c.Plane.Check(ctx, envelope.Credential, p.Binding, "", "", "", "")
	}
	if e != nil {
		return Result{}, Refused
	}
	p.ContextExpires = fact.Expires
	payload, _ := json.Marshal(p)
	req, e := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(payload))
	if e != nil {
		return Result{}, Refused
	}
	req.Header.Set("Authorization", "Bearer "+envelope.Credential)
	req.Header.Set("X-Tenant-ID", p.Tenant)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 8 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(req)
	if e != nil {
		return Result{}, Refused
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Result{}, Refused
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 16385))
	if e != nil || len(data) > 16384 {
		return Result{}, Refused
	}
	var result Result
	if decode(data, &result) != nil {
		return Result{}, Refused
	}
	expected := Identity{Tenant: fact.Tenant, Owner: fact.Owner, Principal: fact.Principal, Organization: fact.Organization, Device: fact.Device, Bot: fact.Bot, Assignment: fact.Assignment, Session: fact.Session}
	now := float64(time.Now().UnixNano()) / 1e9
	if result.Version != 1 || result.RequestID != p.RequestID || result.PreviewOnly == active || result.Identity != expected || !reflect.DeepEqual(result.Action, p.Action) || result.Project != p.Project || !id(result.PolicyRevision) || result.Expires <= now || result.Expires > fact.Expires || !limit(result.Limits) || result.Limits.Bytes > p.Action.Limits.Bytes || result.Limits.Seconds > p.Action.Limits.Seconds || len(result.Reason) == 0 || len(result.Reason) > 256 {
		return Result{}, Refused
	}
	if active && result.Permitted && (result.Limits != p.Action.Limits || p.ExpectedPolicy != "" && result.PolicyRevision != p.ExpectedPolicy) {
		return Result{}, Refused
	}
	switch result.Decision {
	case "allowed", "advisory":
		if !result.Permitted {
			return Result{}, Refused
		}
	case "prohibited", "extreme", "unavailable":
		if result.Permitted || result.Learning {
			return Result{}, Refused
		}
	default:
		return Result{}, Refused
	}
	return result, nil
}
