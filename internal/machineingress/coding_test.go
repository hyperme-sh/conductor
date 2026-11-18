package machineingress

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCodingClaimBindsConfiguredTargetAndNeverRetries(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-workspace", "wrong-owner", "wrong-route", "lost-output", "cancelled", "idle"} {
		t.Run(scenario, func(t *testing.T) {
			s := submission()
			target := CodingTarget{Binding: s.Binding, Workspace: "/project", Class: s.Class, Destination: s.Destination, MaxOutput: s.MaxOutput}
			original, _ := json.Marshal(s)
			sum := sha256.Sum256(original)
			owner, org := uuid.NewString(), uuid.NewString()
			claims, dispatches := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if r.Header.Get("Authorization") != "Bearer hm_msi_test" || r.Header.Get("X-Tenant-ID") != s.Tenant {
					t.Error("credential binding")
				}
				fact := Fact{Version: 1, Request: body["request_id"].(string), Tenant: s.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: s.Device, Bot: s.Bot, Assignment: s.Assignment, Session: s.Session, Expires: float64(time.Now().Unix()) + 10}
				if strings.HasSuffix(r.URL.Path, "/access-context") {
					json.NewEncoder(w).Encode(fact)
					return
				}
				claims++
				if claims > 1 {
					t.Error("claim repeated")
				}
				if body["workspace"] != "/project" {
					t.Error("workspace not bound")
				}
				project := "/project"
				if scenario == "wrong-workspace" {
					project = "/other"
				}
				if scenario == "wrong-owner" {
					fact.Owner = uuid.NewString()
				}
				raw := original
				if scenario == "wrong-route" {
					s.Destination = "other"
					raw, _ = json.Marshal(s)
					sum = sha256.Sum256(raw)
				}
				task := any(map[string]any{"id": s.Task, "assignment_id": s.Assignment, "session_id": s.Session, "capability": "coding", "status": "delivered", "payload": map[string]any{"version": 1, "workspace": project, "device_id": s.Device, "operation_id": s.Operation, "request_sha256": hex.EncodeToString(sum[:]), "submission_base64": base64.StdEncoding.EncodeToString(raw)}})
				if scenario == "idle" {
					task = nil
				}
				json.NewEncoder(w).Encode(map[string]any{"request_id": body["request_id"], "identity": fact, "task": task})
			}))
			defer server.Close()
			c := Client{Origin: server.URL, HTTP: server.Client()}
			result, e := c.runCoding(t.Context(), "hm_msi_test", target, func(ctx context.Context, raw []byte) (json.RawMessage, error) {
				dispatches++
				if string(raw) != string(original) {
					t.Error("original changed")
				}
				if scenario == "lost-output" || scenario == "cancelled" {
					return nil, io.ErrUnexpectedEOF
				}
				return json.RawMessage(`{"choices":[{"message":{"content":"answer"}}]}`), nil
			})
			if strings.HasPrefix(scenario, "wrong-") {
				if e == nil || dispatches != 0 {
					t.Fatal(result, e, dispatches)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if scenario == "idle" {
				if dispatches != 0 || result.State != "idle" {
					t.Fatal(result)
				}
				return
			}
			if dispatches != 1 {
				t.Fatal(dispatches)
			}
			if scenario == "success" {
				if result.State != "output-observed" || len(result.OutputSHA) != 64 {
					t.Fatal(result)
				}
			} else if result.State != "unknown" {
				t.Fatal(result)
			}
		})
	}
}
