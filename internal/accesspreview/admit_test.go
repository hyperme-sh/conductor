//go:build linux

package accesspreview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilitydispatch"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTypedAdmissionSurvivesJournalRestart(t *testing.T) {
	e := sample()
	e.Preview.Action.Operation = "write"
	e.Preview.Template[0].Operation = "write"
	rawArgs, _ := json.Marshal(WriteArguments{ContentHash: strings.Repeat("a", 64), ContentBytes: 12, ObjectHash: strings.Repeat("b", 64), ParentHash: strings.Repeat("c", 64)})
	e.Preview.Action.ArgumentsJSON = string(rawArgs)
	e.Preview.Action.Arguments = fmt.Sprintf("%x", sha256.Sum256(rawArgs))
	p := e.Preview
	owner, org, revision := uuid.NewString(), uuid.NewString(), uuid.NewString()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		json.NewDecoder(r.Body).Decode(&input)
		if strings.HasSuffix(r.URL.Path, "/access-context") {
			json.NewEncoder(w).Encode(machineingress.Fact{Version: 1, Request: input["request_id"].(string), Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session, Expires: float64(time.Now().UnixNano())/1e9 + 10})
			return
		}
		json.NewEncoder(w).Encode(Result{Version: 1, RequestID: p.RequestID, Identity: Identity{Tenant: p.Tenant, Owner: owner, Principal: "owner", Organization: org, Device: p.Device, Bot: p.Bot, Assignment: p.Assignment, Session: p.Session}, Action: p.Action, Project: p.Project, Decision: "allowed", Permitted: true, Reason: "Exact write", Limits: p.Action.Limits, PolicyRevision: revision, Expires: input["context_expires_at"].(float64)})
	}))
	defer server.Close()
	client := Client{Plane: machineingress.Client{Origin: server.URL, HTTP: server.Client()}, Endpoint: server.URL + "/api/v1/aiuditor/access/action-review", HTTP: server.Client()}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(directory, "dispatch.sqlite")
	journal, err := capabilitydispatch.OpenJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	result, err := client.Admit(context.Background(), raw, journal)
	if err != nil || !result.Reserved {
		t.Fatal(result, err)
	}
	journal.Close()
	journal, err = capabilitydispatch.OpenJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err = client.Admit(context.Background(), raw, journal); err == nil {
		t.Fatal("consumed side effect admitted after restart")
	}
}
