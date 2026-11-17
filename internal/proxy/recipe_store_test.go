package proxy

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

var testDraftScope = draftScope{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "actor"}

const testDraftToken = "fixture-draft-only-credential-000001"
const testDraft = `{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"configured","max_output_tokens":12}}`

func setupDraft(t *testing.T) *Proxy {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_RECIPE_STORE_ENABLED", "true")
	t.Setenv("CONDUCTOR_RECIPE_STORE_DIR", dir)
	t.Setenv("CONDUCTOR_RECIPE_TOKEN_TEST", testDraftToken)
	b, _ := json.Marshal([]draftBinding{{testDraftScope, "CONDUCTOR_RECIPE_TOKEN_TEST"}})
	t.Setenv("CONDUCTOR_RECIPE_BINDINGS", string(b))
	return New(config.Config{Cheap: config.Tier{Name: "cheap", Model: "configured", OutputLimit: 99}}, nil, nil)
}
func draftCall(p *Proxy, scope draftScope, action string, revision *string, recipe string, token string) (int, draftResult) {
	input := map[string]any{"tenant_id": scope.Tenant, "organization_id": scope.Organization, "subject": scope.Subject, "action": action}
	if action == "save" {
		input["expected_revision"] = revision
		input["recipe"] = recipe
	}
	data, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/v1/recipes/draft", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Conductor-Recipe-Token", token)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	var result draftResult
	json.Unmarshal(rec.Body.Bytes(), &result)
	return rec.Code, result
}
func TestDraftPersistsAndRejectsStaleUpdates(t *testing.T) {
	p := setupDraft(t)
	if code, r := draftCall(p, testDraftScope, "read", nil, "", testDraftToken); code != 200 || r.Revision != nil || r.Recipe != nil {
		t.Fatalf("empty %d %+v", code, r)
	}
	code, saved := draftCall(p, testDraftScope, "save", nil, testDraft, testDraftToken)
	if code != 200 || saved.Revision == nil {
		t.Fatalf("save %d %+v", code, saved)
	}
	// Recreate the owner, with no retained memory, and load the exact bytes.
	next := New(p.Config, nil, nil)
	code, loaded := draftCall(next, testDraftScope, "read", nil, "", testDraftToken)
	if code != 200 || loaded.Recipe == nil || *loaded.Recipe != testDraft || *loaded.Revision != *saved.Revision {
		t.Fatalf("load %d %+v", code, loaded)
	}
	if code, r := draftCall(next, testDraftScope, "save", nil, testDraft, testDraftToken); code != 409 || r.Recipe != nil {
		t.Fatalf("stale %d %+v", code, r)
	}
	code, updated := draftCall(next, testDraftScope, "save", saved.Revision, testDraft, testDraftToken)
	if code != 200 || *updated.Revision == *saved.Revision {
		t.Fatal("revision not advanced")
	}
	if p.Config.Cheap.OutputLimit != 99 {
		t.Fatal("save activated recipe")
	}
}
func TestDraftConcurrentWritersHaveOneWinner(t *testing.T) {
	p := setupDraft(t)
	_, saved := draftCall(p, testDraftScope, "save", nil, testDraft, testDraftToken)
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := draftCall(New(p.Config, nil, nil), testDraftScope, "save", saved.Revision, testDraft, testDraftToken)
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
}
func TestDraftScopeAndCredentialsFailClosed(t *testing.T) {
	p := setupDraft(t)
	for _, scope := range []draftScope{{testDraftScope.Tenant, testDraftScope.Organization, "other"}, {testDraftScope.Organization, testDraftScope.Organization, "actor"}, {testDraftScope.Tenant, testDraftScope.Tenant, "actor"}} {
		if code, _ := draftCall(p, scope, "save", nil, testDraft, testDraftToken); code != 403 {
			t.Fatal(code)
		}
	}
	t.Setenv(config.ProxyTokenEnv, "general-proxy-token")
	if code, _ := draftCall(p, testDraftScope, "save", nil, testDraft, "general-proxy-token"); code != 403 {
		t.Fatal(code)
	}
	t.Setenv("CONDUCTOR_RECIPE_BINDINGS", fmt.Sprintf(`[{"tenant_id":%q,"organization_id":%q,"subject":"actor","token_env":"CONDUCTOR_RECIPE_TOKEN_TEST"},{"tenant_id":%q,"organization_id":%q,"subject":"other","token_env":"CONDUCTOR_RECIPE_TOKEN_TEST"}]`, testDraftScope.Tenant, testDraftScope.Organization, testDraftScope.Tenant, testDraftScope.Organization))
	if code, _ := draftCall(p, testDraftScope, "read", nil, "", testDraftToken); code != 403 {
		t.Fatal("shared credential accepted")
	}
}
func TestDraftValidationRefusalPreservesStoredValue(t *testing.T) {
	p := setupDraft(t)
	_, saved := draftCall(p, testDraftScope, "save", nil, testDraft, testDraftToken)
	if code, r := draftCall(p, testDraftScope, "save", saved.Revision, strings.Replace(testDraft, "configured", "private-other", 1), testDraftToken); code != 422 || r.Field != "cheap-model" || r.Recipe != nil {
		t.Fatalf("%d %+v", code, r)
	}
	_, loaded := draftCall(p, testDraftScope, "read", nil, "", testDraftToken)
	if *loaded.Recipe != testDraft || *loaded.Revision != *saved.Revision {
		t.Fatal("invalid overwrite")
	}
	t.Setenv("CONDUCTOR_RECIPE_STORE_ENABLED", "")
	if code, _ := draftCall(p, testDraftScope, "read", nil, "", testDraftToken); code != 503 {
		t.Fatal(code)
	}
}
func TestDraftDirectoryMustBePrivateAndNotLinked(t *testing.T) {
	p := setupDraft(t)
	dir := os.Getenv("CONDUCTOR_RECIPE_STORE_DIR")
	os.Chmod(dir, 0755)
	if code, _ := draftCall(p, testDraftScope, "read", nil, "", testDraftToken); code != 503 {
		t.Fatal(code)
	}
	os.Chmod(dir, 0700)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONDUCTOR_RECIPE_STORE_DIR", link)
	if code, _ := draftCall(p, testDraftScope, "read", nil, "", testDraftToken); code != 503 {
		t.Fatal(code)
	}
}

// Response writing must never hold the configuration reload mutex.
type draftDeadlineWriter struct {
	*httptest.ResponseRecorder
	p        *Proxy
	t        *testing.T
	deadline time.Time
}

func (w *draftDeadlineWriter) SetWriteDeadline(value time.Time) error { w.deadline = value; return nil }
func (w *draftDeadlineWriter) Write(data []byte) (int, error) {
	if w.deadline.IsZero() || time.Until(w.deadline) > 6*time.Second {
		w.t.Fatal("missing bounded write deadline")
	}
	if !w.p.recipes.mu.TryLock() {
		w.t.Fatal("response holds reload mutex")
	}
	w.p.recipes.mu.Unlock()
	return w.ResponseRecorder.Write(data)
}
func TestDraftResponseDoesNotHoldReloadLock(t *testing.T) {
	p := setupDraft(t)
	p.EnableRecipeReload("")
	body, _ := json.Marshal(map[string]any{"action": "read", "tenant_id": testDraftScope.Tenant, "organization_id": testDraftScope.Organization, "subject": testDraftScope.Subject})
	r := httptest.NewRequest("POST", "/v1/recipes/draft", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Conductor-Recipe-Token", testDraftToken)
	w := &draftDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), p: p, t: t}
	p.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
