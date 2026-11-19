package accesspreview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sample() Envelope {
	binding := machineingress.Binding{Version: 1, Tenant: uuid.NewString(), Device: uuid.NewString(), Bot: uuid.NewString(), Assignment: uuid.NewString(), Session: uuid.NewString()}
	rule := Rule{Operation: "read", Target: "/project/file", Limits: Limits{Bytes: 1024, Seconds: 10}}
	return Envelope{Credential: "hm_msi_private", Preview: Request{Binding: binding, RequestID: uuid.NewString(), Project: "/project", TemplateVersion: 1, Template: []Rule{rule}, Action: Action{Rule: rule, Arguments: strings.Repeat("a", 64)}}}
}
func TestDualOwnerPreviewBindings(t *testing.T) {
	for _, scenario := range []string{"advisory", "prohibited", "wrong-owner", "wrong-action", "expired", "execution", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			envelope := sample()
			p := envelope.Preview
			owner, org := uuid.NewString(), uuid.NewString()
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer hm_msi_private" || r.Header.Get("X-Tenant-ID") != p.Tenant {
					t.Error("credential binding missing")
				}
				if strings.HasSuffix(r.URL.Path, "/machine/context") {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					json.NewEncoder(w).Encode(machineingress.Fact{Version: 1, Request: body["request_id"].(string), Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session, Expires: float64(time.Now().UnixNano())/1e9 + 10})
					return
				}
				calls++
				if r.URL.Path != "/api/v1/aiuditor/access/preview" {
					t.Error(r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), "hm_msi_") {
					t.Error("credential in policy body")
				}
				var sent Request
				if json.Unmarshal(raw, &sent) != nil || sent.ContextExpires == 0 {
					t.Error("missing bounded context")
				}
				result := Result{Version: 1, RequestID: p.RequestID, PreviewOnly: true, Identity: Identity{Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session}, Action: p.Action, Project: p.Project, Decision: "advisory", Permitted: true, Reason: "Review during dreaming", Limits: p.Action.Limits, PolicyRevision: uuid.NewString(), Expires: sent.ContextExpires}
				switch scenario {
				case "prohibited":
					result.Decision = "prohibited"
					result.Permitted = false
				case "wrong-owner":
					result.Identity.Owner = uuid.NewString()
				case "wrong-action":
					result.Action.Target = "/other"
				case "expired":
					result.Expires = 0
				case "execution":
					result.PreviewOnly = false
				case "redirect":
					w.Header().Set("Location", "https://other.test/")
					w.WriteHeader(302)
					return
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			client := Client{Plane: machineingress.Client{Origin: server.URL, HTTP: server.Client()}, Endpoint: server.URL + "/api/v1/aiuditor/access/preview", HTTP: server.Client()}
			raw, _ := json.Marshal(envelope)
			result, err := client.Preview(context.Background(), raw)
			if scenario == "advisory" || scenario == "prohibited" {
				if err != nil || result.Decision != scenario {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid response")
			}
			if calls != 1 {
				t.Fatal(calls)
			}
		})
	}
}
func TestUntrustedEndpointsAndCredentialsAreNotUsed(t *testing.T) {
	envelope := sample()
	raw, _ := json.Marshal(envelope)
	for _, endpoint := range []string{"http://localhost/api/v1/aiuditor/access/preview", "https://user:password@example.test/api/v1/aiuditor/access/preview", "https://example.test/other"} {
		if _, err := (Client{Endpoint: endpoint}).Preview(context.Background(), raw); err == nil {
			t.Fatal(endpoint)
		}
	}
}

func TestActiveReadBindings(t *testing.T) {
	for _, scenario := range []string{"advisory", "prohibited", "wrong-owner", "wrong-action", "expired", "execution", "redirect", "stale-policy", "paused"} {
		t.Run(scenario, func(t *testing.T) {
			envelope := sample()
			envelope.Preview.ExpectedPolicy = uuid.NewString()
			p := envelope.Preview
			owner, org := uuid.NewString(), uuid.NewString()
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer hm_msi_private" || r.Header.Get("X-Tenant-ID") != p.Tenant {
					t.Error("credential binding missing")
				}
				if strings.HasSuffix(r.URL.Path, "/machine/access-context") {
					if scenario == "paused" {
						w.WriteHeader(403)
						return
					}
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					json.NewEncoder(w).Encode(machineingress.Fact{Version: 1, Request: body["request_id"].(string), Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session, Expires: float64(time.Now().UnixNano())/1e9 + 10})
					return
				}
				calls++
				if r.URL.Path != "/api/v1/aiuditor/access/read-review" {
					t.Error(r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), "hm_msi_") {
					t.Error("credential in policy body")
				}
				var sent Request
				if json.Unmarshal(raw, &sent) != nil || sent.ContextExpires == 0 {
					t.Error("missing bounded context")
				}
				result := Result{Version: 1, RequestID: p.RequestID, PreviewOnly: false, Identity: Identity{Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session}, Action: p.Action, Project: p.Project, Decision: "advisory", Permitted: true, Reason: "Review during dreaming", Limits: p.Action.Limits, PolicyRevision: p.ExpectedPolicy, Expires: sent.ContextExpires}
				switch scenario {
				case "prohibited":
					result.Decision = "prohibited"
					result.Permitted = false
				case "wrong-owner":
					result.Identity.Owner = uuid.NewString()
				case "wrong-action":
					result.Action.Target = "/other"
				case "expired":
					result.Expires = 0
				case "execution":
					result.PreviewOnly = true
				case "stale-policy":
					result.PolicyRevision = uuid.NewString()
				case "redirect":
					w.Header().Set("Location", "https://other.test/")
					w.WriteHeader(302)
					return
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			client := Client{Plane: machineingress.Client{Origin: server.URL, HTTP: server.Client()}, Endpoint: server.URL + "/api/v1/aiuditor/access/read-review", HTTP: server.Client()}
			raw, _ := json.Marshal(envelope)
			result, err := client.ReviewRead(context.Background(), raw)
			if scenario == "advisory" || scenario == "prohibited" {
				if err != nil || result.Decision != scenario {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid response")
			}
			expectedCalls := 1
			if scenario == "paused" {
				expectedCalls = 0
			}
			if calls != expectedCalls {
				t.Fatal(calls)
			}
		})
	}
}

func TestTypedWriteBindings(t *testing.T) {
	for _, scenario := range []string{"advisory", "prohibited", "wrong-owner", "wrong-action", "expired", "execution", "redirect", "stale-policy", "paused"} {
		t.Run(scenario, func(t *testing.T) {
			envelope := sample()
			envelope.Preview.Action.Operation = "write"
			envelope.Preview.Template[0].Operation = "write"
			args, _ := json.Marshal(WriteArguments{ContentHash: strings.Repeat("a", 64), ContentBytes: 12, ObjectHash: strings.Repeat("b", 64), ParentHash: strings.Repeat("c", 64)})
			envelope.Preview.Action.ArgumentsJSON = string(args)
			envelope.Preview.Action.Arguments = fmt.Sprintf("%x", sha256.Sum256(args))
			envelope.Preview.ExpectedPolicy = uuid.NewString()
			p := envelope.Preview
			owner, org := uuid.NewString(), uuid.NewString()
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer hm_msi_private" || r.Header.Get("X-Tenant-ID") != p.Tenant {
					t.Error("credential binding missing")
				}
				if strings.HasSuffix(r.URL.Path, "/machine/access-context") {
					if scenario == "paused" {
						w.WriteHeader(403)
						return
					}
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					json.NewEncoder(w).Encode(machineingress.Fact{Version: 1, Request: body["request_id"].(string), Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session, Expires: float64(time.Now().UnixNano())/1e9 + 10})
					return
				}
				calls++
				if r.URL.Path != "/api/v1/aiuditor/access/action-review" {
					t.Error(r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), "hm_msi_") {
					t.Error("credential in policy body")
				}
				var sent Request
				if json.Unmarshal(raw, &sent) != nil || sent.ContextExpires == 0 {
					t.Error("missing bounded context")
				}
				result := Result{Version: 1, RequestID: p.RequestID, PreviewOnly: false, Identity: Identity{Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session}, Action: p.Action, Project: p.Project, Decision: "advisory", Permitted: true, Reason: "Review during dreaming", Limits: p.Action.Limits, PolicyRevision: p.ExpectedPolicy, Expires: sent.ContextExpires}
				switch scenario {
				case "prohibited":
					result.Decision = "prohibited"
					result.Permitted = false
				case "wrong-owner":
					result.Identity.Owner = uuid.NewString()
				case "wrong-action":
					result.Action.Target = "/other"
				case "expired":
					result.Expires = 0
				case "execution":
					result.PreviewOnly = true
				case "stale-policy":
					result.PolicyRevision = uuid.NewString()
				case "redirect":
					w.Header().Set("Location", "https://other.test/")
					w.WriteHeader(302)
					return
				}
				json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			client := Client{Plane: machineingress.Client{Origin: server.URL, HTTP: server.Client()}, Endpoint: server.URL + "/api/v1/aiuditor/access/action-review", HTTP: server.Client()}
			raw, _ := json.Marshal(envelope)
			result, err := client.ReviewAction(context.Background(), raw)
			if scenario == "advisory" || scenario == "prohibited" {
				if err != nil || result.Decision != scenario {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid response")
			}
			expectedCalls := 1
			if scenario == "paused" {
				expectedCalls = 0
			}
			if calls != expectedCalls {
				t.Fatal(calls)
			}
		})
	}
}
