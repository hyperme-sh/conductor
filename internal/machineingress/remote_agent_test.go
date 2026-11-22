package machineingress

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/taskscope"
)

func TestRemoteAgentAuthorityBinding(t *testing.T) {
	s := submission()
	s.Enrollment = uuid.NewString()
	host := uuid.NewString()
	digest := strings.Repeat("a", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1/hypermesh/bots/" + s.Bot + "/remote-agent/operation"
		if r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer hm_rag_fixture" ||
			r.Header.Get("X-Tenant-ID") != s.Tenant || r.Header.Get("X-Conductor-Spend-Admission") != "" {
			t.Error("unexpected remote-agent authorization request")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var nonce struct {
			Version    int    `json:"version"`
			Request    string `json:"request_id"`
			Enrollment string `json:"enrollment_id"`
			Device     string `json:"device_id"`
			Assignment string `json:"assignment_id"`
			Session    string `json:"session_id"`
			Task       string `json:"task_id"`
			Operation  string `json:"operation_id"`
			Digest     string `json:"request_sha256"`
			Command    string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&nonce); err != nil ||
			nonce.Version != 1 || nonce.Enrollment != s.Enrollment || nonce.Device != s.Device ||
			nonce.Assignment != s.Assignment || nonce.Session != s.Session ||
			nonce.Task != s.Task || nonce.Operation != s.Operation || nonce.Digest != digest ||
			nonce.Command != "begin" {
			t.Error("missing exact operation binding", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(Fact{
			Version: 1, Request: nonce.Request, Tenant: s.Tenant, Bot: s.Bot,
			Owner: uuid.NewString(), Organization: uuid.NewString(), Principal: "owner",
			Device: host, CallerDevice: s.Device, CallerEnrollment: s.Enrollment,
			Assignment: s.Assignment, Session: s.Session, Task: s.Task,
			Operation: s.Operation, Digest: digest, State: "active",
			Expires: float64(time.Now().Unix()) + 9,
		})
	}))
	defer server.Close()
	client := Client{Origin: server.URL, HTTP: server.Client(), RemoteAgent: true}
	ctx := context.Background()
	for _, token := range []string{"hm_msi_phone", "hm_bot_test", "hm_rag_"} {
		if _, err := client.Check(ctx, token, s.Binding, s.Task, s.Operation, digest, "begin"); err == nil {
			t.Fatal("wrong credential audience was accepted")
		}
	}
	if _, err := client.Check(ctx, "hm_rag_fixture", s.Binding, "", "", "", ""); err == nil {
		t.Fatal("remote agent accepted preview context")
	}
	fact, err := client.Check(ctx, "hm_rag_fixture", s.Binding, s.Task, s.Operation, digest, "begin")
	if err != nil || fact.Device != host {
		t.Fatalf("expected independent execution host, got %v, %v", fact, err)
	}
}

func TestRemoteAgentModelLeaseDispatch(t *testing.T) {
	s := submission()
	s.Enrollment = uuid.NewString()
	owner, org, host := uuid.NewString(), uuid.NewString(), uuid.NewString()
	lease := ModelLease{ID: uuid.NewString(), Origin: "https://gateway.test.example", RenterKey: "fixture-renter-key"}
	for _, returned := range []string{"", uuid.NewString(), lease.ID} {
		t.Run(returned, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Nonce   string `json:"request_id"`
					Digest  string `json:"request_sha256"`
					Lease   string `json:"model_lease_id"`
					Command string `json:"command"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Lease != lease.ID ||
					r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "Bearer hm_rag_fixture" {
					t.Error("Plane request differs from the admitted lease")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				state := "active"
				if input.Command == "cancel" {
					state = "unknown"
				}
				_ = json.NewEncoder(w).Encode(Fact{Version: 1, Request: input.Nonce, Tenant: s.Tenant, Bot: s.Bot,
					Assignment: s.Assignment, Session: s.Session, Operation: s.Operation, Task: s.Task, Digest: input.Digest,
					Owner: owner, Organization: org, Device: host, Principal: "owner", CallerDevice: s.Device, CallerEnrollment: s.Enrollment,
					Expires: float64(time.Now().Unix()) + 9, State: state, ModelLease: returned})
			}))
			defer server.Close()
			called := false
			h, err := New(Client{Origin: server.URL, HTTP: server.Client(), RemoteAgent: true, ModelLease: lease},
				s.Tenant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					scope := taskscope.From(r.Context())
					if scope == nil || scope.ModelLease != lease.ID || r.Header.Get("X-Hypermesh-Lease-Id") != lease.ID ||
						r.Header.Get("X-Tenant-ID") != s.Tenant || r.Header.Get("X-Api-Key") != lease.RenterKey ||
						r.Header.Get("X-Conductor-Credential-Origin") != lease.Origin || r.Header.Get("Authorization") != "" {
						t.Error("dispatch did not use the authenticated model lease and gateway credential")
					}
					w.WriteHeader(http.StatusNoContent)
				}), "X-Proxy-Token", func() string { return "proxy-fixture" })
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(s)
			req := httptest.NewRequest(http.MethodPost, "/v1/remote-agent/prompt-submit", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer hm_rag_fixture")
			req.Header.Set("X-Api-Key", "untrusted")
			req.Header.Set("X-Hypermesh-Lease-Id", uuid.NewString())
			reply := httptest.NewRecorder()
			h.ServeHTTP(reply, req)
			want := http.StatusForbidden
			if returned == lease.ID {
				want = http.StatusNoContent
			}
			if reply.Code != want || called != (returned == lease.ID) {
				t.Fatalf("dispatch %v, status %d, want %d", called, reply.Code, want)
			}
		})
	}
}
