package businesstool

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const fixtureKey = "ci-only-workspace-binding-signing-key"

func binding(provider string) Binding {
	return Binding{
		TenantID: "10000000-0000-0000-0000-000000000001", WorkspaceID: "20000000-0000-0000-0000-000000000002",
		ConnectionID: "30000000-0000-0000-0000-000000000003", GrantID: "40000000-0000-0000-0000-000000000004",
		Provider: provider, Target: "configured-target", ActorKind: "person", ActorID: "operator",
	}
}

func invoke(runner Runner, tool, args string) (Result, error) {
	return runner.Run(context.Background(), Call{1, tool, json.RawMessage(args)})
}

type reviewFunc func(Binding, string) error

func (f reviewFunc) Authorize(_ context.Context, binding Binding, operation string) error {
	return f(binding, operation)
}

func TestAuthoritySignsAndBindsFreshPerOperationDecisions(t *testing.T) {
	want := binding("odoo")
	var previous string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != authorizationPath {
			t.Error("authority request used unexpected route")
			http.Error(w, "wrong route", http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(r.Body)
		hash := hmac.New(sha256.New, []byte(fixtureKey))
		hash.Write([]byte(signaturePrefix))
		hash.Write(raw)
		var call authorityRequest
		if err != nil || json.Unmarshal(raw, &call) != nil || call.Binding != want ||
			!isUUID(call.RequestID) || call.RequestID == previous || call.Version != 1 ||
			call.IssuedAt < time.Now().Unix()-5 ||
			r.Header.Get("X-Workspace-Connection-Signature") != "sha256="+hex.EncodeToString(hash.Sum(nil)) {
			t.Error("authority request changed its signed scope or freshness")
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		previous = call.RequestID
		id, reason := call.RequestID, "allowed"
		switch call.Operation {
		case "draft":
			id = want.GrantID
		case "send":
			reason = "operation_denied"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "request_id": id, "allowed": true, "reason": reason})
	}))
	defer server.Close()
	reviewer, err := NewAuthority(server.URL, fixtureKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = reviewer.Authorize(context.Background(), want, "read"); err != nil {
		t.Fatal(err)
	}
	if err = reviewer.Authorize(context.Background(), want, "draft"); err != Denied {
		t.Fatalf("unbound authorization response accepted: %v", err)
	}
	if err = reviewer.Authorize(context.Background(), want, "send"); err != Denied {
		t.Fatalf("contradictory authorization response accepted: %v", err)
	}
}

func TestOdooScopesReadsAndReturnsOnlyStableDrafts(t *testing.T) {
	scope := binding("odoo")
	var decisions []string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer ci-odoo-reader" ||
			r.Header.Get("X-Support-Workspace") != scope.WorkspaceID || r.Header.Get("X-Support-Role") != "" {
			t.Error("provider request exceeded reader scope")
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/crm/leads":
			if r.URL.Query().Get("q") != "trial&company=other" || r.URL.Query().Get("limit") != "2" ||
				r.URL.Query().Get("company") != "" {
				t.Error("search text interfered with request scope")
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"42","name":"Trial","version":"v1","email":"private@example.invalid","unknown":"restricted"}]}`)
		case "/v1/crm/leads/42":
			_, _ = io.WriteString(w, `{"lead":{"id":"42","name":"Trial","version":"v1"}}`)
		case "/v1/support/cases":
			_, _ = io.WriteString(w, `{"cases":[{"id":"case-1","subject":"First case","status":"open","version":"v3","messages":[{"body":"private"}]}],"next_cursor":"case-next"}`)
		case "/v1/support/cases/case-1":
			_, _ = io.WriteString(w, `{"case":{"id":"case-1","subject":"First case","status":"open","version":"v3","messages":[{"id":"note-1","actor":"customer","body":"Untrusted reply","created_at":"2026-11-25T12:00:00Z"}]}}`)
		case "/v1/crm/leads/99":
			http.Error(w, "restricted upstream detail", http.StatusForbidden)
		default:
			t.Error("unexpected provider operation")
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	odoo, err := NewOdoo(server.URL, "ci-odoo-reader", nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Binding: scope, Odoo: odoo, Review: reviewFunc(func(actual Binding, operation string) error {
		if actual != scope {
			t.Fatal("model input changed the authorization scope")
		}
		decisions = append(decisions, operation)
		return nil
	})}
	list, err := invoke(runner, "odoo.leads.search", `{"query":"trial&company=other","limit":2}`)
	encoded, _ := json.Marshal(list)
	if err != nil || !list.UntrustedData || strings.Contains(string(encoded), "private@example") || strings.Contains(string(encoded), "restricted") {
		t.Fatalf("lead list was unavailable or included restricted fields: %v", err)
	}
	cases, err := invoke(runner, "odoo.cases.search", `{"limit":2}`)
	if err != nil || len(cases.Data.(caseList).Cases[0].Messages) != 0 || *cases.Data.(caseList).NextCursor != "case-next" {
		t.Fatalf("case search did not preserve its response boundary: %v", err)
	}
	detail, err := invoke(runner, "odoo.cases.read", `{"id":"case-1"}`)
	if err != nil || detail.Data.(Case).Messages[0].Body != "Untrusted reply" {
		t.Fatalf("case detail was unavailable: %v", err)
	}
	if _, err = invoke(runner, "odoo.leads.read", `{"id":"99"}`); err != Denied {
		t.Fatalf("restricted lead was not denied: %v", err)
	}
	if _, err = invoke(runner, "odoo.leads.draft", `{"id":"42","expected_version":"old","action":"route","text":"Review"}`); err != StaleRecord {
		t.Fatalf("stale draft was not denied: %v", err)
	}
	draft := `{"id":"42","expected_version":"v1","action":"route","text":"Review"}`
	first, err := invoke(runner, "odoo.leads.draft", draft)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := invoke(runner, "odoo.leads.draft", draft)
	if err != nil || !reflect.DeepEqual(first, retry) || len(first.Data.(proposal).OperationID) != 64 {
		t.Fatalf("draft did not reconcile to the same operation: %v", err)
	}
	if calls != 7 || !slices.Equal(decisions, []string{"read", "read", "read", "read", "read", "read", "draft", "read", "draft"}) {
		t.Fatalf("unexpected operation sequence: %v, %d calls", decisions, calls)
	}
	for _, test := range [][2]string{
		{"odoo.model.execute", `{"id":"42"}`},
		{"odoo.leads.read", `{"id":"42","company_id":"other"}`},
		{"odoo.leads.read", `{"id":"42","fields":["email"]}`},
		{"odoo.cases.read", `{"id":"../leads/42"}`},
		{"odoo.cases.draft", `{"id":"case-1","expected_version":"v3","action":"send","text":"No"}`},
	} {
		if _, err = invoke(runner, test[0], test[1]); err != Invalid {
			t.Errorf("unsupported argument or action accepted for %s", test[0])
		}
	}
	if calls != 7 {
		t.Fatal("invalid calls reached Odoo")
	}
	runner.Review = reviewFunc(func(_ Binding, operation string) error {
		if operation == "draft" {
			return Denied
		}
		return nil
	})
	if _, err = invoke(runner, "odoo.leads.draft", draft); err != Denied {
		t.Fatalf("a read-only grant issued a draft: %v", err)
	}
}

func TestOutlookFixtureScopesHistoryAndPersistsSynchronization(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "outlook.json")
	state := filepath.Join(dir, "state.db")
	asOf := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	f := outlookFixture{Version: 1, Mailbox: "configured-target", Generation: "g1", AsOf: asOf,
		Messages: []fixtureMessage{
			{1, "inbox", "old", "thread-other", asOf.AddDate(0, 0, -31), "Older than window", ""},
			{2, "inbox", "id-1", "thread-1", asOf.Add(-2 * time.Hour), "First", "First private body"},
			{3, "archive", "private", "thread-2", asOf.Add(-time.Hour), "Archive", "Archive body"},
			{4, "inbox", "id-1", "thread-1", asOf.Add(-2 * time.Hour), "First", "First private body"},
			{5, "inbox", "id-2", "thread-1", asOf, "Second", "Second private body"},
		},
	}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(f)
		if err != nil || os.WriteFile(fixture, raw, 0600) != nil {
			t.Fatal("fixture could not be written")
		}
	}
	write()
	outlook, err := NewOutlook(fixture, state, []string{"inbox"})
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Binding: binding("outlook"), Outlook: outlook, Review: reviewFunc(func(_ Binding, op string) error {
		if op != "read" {
			t.Fatal("mailbox tool requested a write permission")
		}
		return nil
	})}
	list, err := invoke(runner, "outlook.messages.list", `{"folder":"inbox","limit":1}`)
	if err != nil || !list.UntrustedData || list.Data.(mailPage).NextBefore != "id-2" || list.Data.(mailPage).Items[0].Body != "" {
		t.Fatalf("mail list was unavailable or included a body: %v", err)
	}
	thread, err := invoke(runner, "outlook.threads.read", `{"folder":"inbox","thread_id":"thread-1"}`)
	if err != nil || len(thread.Data.(mailPage).Items) != 2 || thread.Data.(mailPage).Items[0].Body != "First private body" {
		t.Fatalf("wrong thread result: %v", err)
	}
	if _, err = invoke(runner, "outlook.messages.list", `{"folder":"archive"}`); err != Invalid {
		t.Fatalf("unconfigured folder was readable: %v", err)
	}
	if _, err = invoke(runner, "outlook.messages.list", `{"folder":"inbox","mailbox":"second"}`); err != Invalid {
		t.Fatalf("model-supplied mailbox accepted: %v", err)
	}
	syncCall := func(slot string, limit string) (syncPage, error) {
		t.Helper()
		result, err := invoke(runner, "outlook.messages.sync", `{"folder":"inbox","operation_id":"50000000-0000-0000-0000-00000000000`+slot+`","limit":`+limit+`}`)
		if err != nil {
			return syncPage{}, err
		}
		return result.Data.(syncPage), nil
	}
	first, err := syncCall("1", "1")
	if err != nil || !first.More || len(first.Items) != 1 || first.Items[0].ID != "id-1" {
		t.Fatalf("unexpected first synchronization: %#v, %v", first, err)
	}
	runner.Outlook, err = NewOutlook(fixture, state, []string{"inbox"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := syncCall("2", "1")
	if err != nil || second.More || len(second.Items) != 1 || second.Items[0].ID != "id-2" {
		t.Fatalf("unexpected synchronization after restart: %#v, %v", second, err)
	}
	retry, err := syncCall("1", "1")
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatalf("retry lost the original batch: %#v, %v", retry, err)
	}
	if _, err = syncCall("1", "2"); err != OperationChanged {
		t.Fatalf("changed retry arguments accepted: %v", err)
	}
	f.Generation = "g2"
	f.Messages = append(f.Messages, fixtureMessage{6, "inbox", "id-3", "thread-3", asOf, "Third", "Third private body"})
	write()
	reset, err := syncCall("3", "3")
	if err != nil || !reset.CursorReset || len(reset.Items) != 1 || reset.Items[0].ID != "id-3" {
		t.Fatalf("cursor rollover re-emitted old messages: %#v, %v", reset, err)
	}
	runner.Binding.WorkspaceID = "20000000-0000-0000-0000-000000000008"
	other, err := syncCall("3", "3")
	if err != nil || other.CursorReset || len(other.Items) != 3 {
		t.Fatalf("second workspace reused the first cursor: %#v, %v", other, err)
	}
	f.Mailbox = "ungranted-mailbox"
	write()
	if _, err = syncCall("4", "3"); err != Denied {
		t.Fatalf("second mailbox was readable: %v", err)
	}
	f.Mailbox = runner.Binding.Target
	for _, status := range []Status{ConsentRequired, Revoked, Throttled, StaleSync} {
		f.Status = status
		write()
		if _, err = syncCall("4", "3"); err != status {
			t.Errorf("fixture state %s was lost: %v", status, err)
		}
	}
	raw, err := os.ReadFile(state)
	if err != nil || strings.Contains(string(raw), "private body") {
		t.Fatal("private message body was persisted in the synchronization state")
	}
}
