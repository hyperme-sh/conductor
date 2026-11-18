package accesspreview

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"time"
)

type DreamRecord struct {
	RuleHash       string  `json:"rule_hash"`
	PolicyRevision string  `json:"policy_revision"`
	Action         Action  `json:"action"`
	LastUse        float64 `json:"last_use"`
	Expires        float64 `json:"expires_at"`
	Pending        bool    `json:"review_pending"`
}
type DreamRequest struct {
	machineingress.Binding
	RequestID string        `json:"request_id"`
	Project   string        `json:"project"`
	Records   []DreamRecord `json:"records"`
}
type DreamOutcome struct {
	RuleHash string  `json:"rule_hash"`
	Kind     string  `json:"kind"`
	Status   string  `json:"status"`
	Reason   string  `json:"reason"`
	Before   Action  `json:"before"`
	After    *Action `json:"after"`
}
type DreamResult struct {
	Version        int            `json:"version"`
	RequestID      string         `json:"request_id"`
	Project        string         `json:"project"`
	Identity       Identity       `json:"identity"`
	PolicyRevision string         `json:"policy_revision"`
	Outcomes       []DreamOutcome `json:"outcomes"`
}

func (c Client) Dream(ctx context.Context, raw []byte) (DreamResult, error) {
	var envelope struct {
		Credential string       `json:"credential"`
		Dream      DreamRequest `json:"dream"`
	}
	if len(raw) > 131072 || decode(raw, &envelope) != nil {
		return DreamResult{}, Refused
	}
	p := envelope.Dream
	if !p.Binding.Valid() || !id(p.RequestID) || !canonicalFile(p.Project) || len(p.Records) < 1 || len(p.Records) > 8 {
		return DreamResult{}, Refused
	}
	seen := map[string]bool{}
	for _, r := range p.Records {
		if !hash(r.RuleHash) || seen[r.RuleHash] || !id(r.PolicyRevision) || !limit(r.Action.Limits) || r.LastUse <= 0 || r.Expires <= 0 || math.IsNaN(r.LastUse) || math.IsNaN(r.Expires) {
			return DreamResult{}, Refused
		}
		seen[r.RuleHash] = true
		if r.Action.Operation == "read" {
			if r.Action.ArgumentsJSON != "" || !hash(r.Action.Arguments) || !canonicalFile(r.Action.Target) {
				return DreamResult{}, Refused
			}
		} else if !validTyped(Request{Project: p.Project, Action: r.Action}) {
			return DreamResult{}, Refused
		}
	}
	endpoint, e := url.Parse(c.Endpoint)
	if e != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/api/v1/aiuditor/access/dream" {
		return DreamResult{}, Refused
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fact, e := c.Plane.ActiveContext(ctx, envelope.Credential, p.Binding)
	if e != nil {
		return DreamResult{}, Refused
	}
	payload, _ := json.Marshal(p)
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint.String(), bytes.NewReader(payload))
	if e != nil {
		return DreamResult{}, Refused
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
		return DreamResult{}, Refused
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return DreamResult{}, Refused
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 131073))
	if e != nil || len(data) > 131072 {
		return DreamResult{}, Refused
	}
	var result DreamResult
	expected := Identity{Tenant: fact.Tenant, Owner: fact.Owner, Principal: fact.Principal, Organization: fact.Organization, Device: fact.Device, Bot: fact.Bot, Assignment: fact.Assignment, Session: fact.Session}
	if decode(data, &result) != nil || result.Version != 1 || result.RequestID != p.RequestID || result.Project != p.Project || result.Identity != expected || !id(result.PolicyRevision) || len(result.Outcomes) != len(p.Records) {
		return DreamResult{}, Refused
	}
	for i, out := range result.Outcomes {
		if out.RuleHash != p.Records[i].RuleHash || !reflect.DeepEqual(out.Before, p.Records[i].Action) || len(out.Reason) == 0 || len(out.Reason) > 256 {
			return DreamResult{}, Refused
		}
		if out.Status != "applied" && out.Status != "superseded" && out.Status != "failed" {
			return DreamResult{}, Refused
		}
		if out.Kind != "retain" && out.Kind != "narrow" && out.Kind != "expire" && out.Kind != "revoke" {
			return DreamResult{}, Refused
		}
		if out.After != nil && !reflect.DeepEqual(*out.After, out.Before) {
			return DreamResult{}, Refused
		}
		if out.Kind == "retain" && out.After == nil {
			return DreamResult{}, Refused
		}
	}
	return result, nil
}
