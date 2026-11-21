package machineingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
