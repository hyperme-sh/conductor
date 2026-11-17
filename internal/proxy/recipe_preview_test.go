package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

func TestNewTaskRecipePreview(t *testing.T) {
	t.Setenv("CONDUCTOR_RECIPE_PREVIEW_ENABLED", "1")
	calls := 0
	tokenResult := `{"count":10,"max_model_len":32}`
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/tokenize" {
			t.Errorf("generation or other call: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer provider-secret" || r.Header.Get(config.ProxyTokenHeader) != "" {
			t.Error("credential separation failed")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "cheap" || body["max_tokens"] != nil {
			t.Error("wrong tokenizer body")
		}
		fmt.Fprint(w, tokenResult)
	}))
	defer provider.Close()
	dir := t.TempDir()
	cfg := loadTestConfig(t, dir, provider.URL+"/v1", provider.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	t.Setenv(config.ProxyTokenEnv, "preview-secret")
	cfg.Cheap.InputCounter, cfg.Frontier.InputCounter = "vllm", "vllm"
	cfg.Cheap.OutputLimit = 77
	cfg.Cheap.Model = "cheap"
	journal := openJournal(t, dir)
	p := New(cfg, nil, journal)
	p.EnableRecipeReload("/must-not-reload")
	recipe := `{"schema":"hypermesh.conductor.recipe.v1","max_task_tokens":40,"max_task_output_tokens":12,"cheap":{"model":"cheap","max_output_tokens":8,"context_window_tokens":64},"frontier":{"model":"frontier","max_output_tokens":8,"context_window_tokens":64}}`
	invoke := func(method, token, doc, chat string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"recipe": json.RawMessage(doc), "tier": "cheap", "chat": json.RawMessage(chat)})
		r := httptest.NewRequest(method, "/v1/config/preview", bytes.NewReader(raw))
		r.Header.Set(config.ProxyTokenHeader, token)
		r.Header.Set("Authorization", "Bearer provider-secret")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	chat := `{"messages":[{"role":"user","content":"private prompt"}],"n":2,"max_tokens":4}`
	if w := invoke("POST", "", recipe, chat); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := invoke("GET", "preview-secret", recipe, chat); w.Code != 405 {
		t.Fatal(w.Code)
	}
	t.Setenv("CONDUCTOR_RECIPE_PREVIEW_ENABLED", "0")
	if w := invoke("POST", "preview-secret", recipe, chat); w.Code != 404 {
		t.Fatal(w.Code)
	}
	t.Setenv("CONDUCTOR_RECIPE_PREVIEW_ENABLED", "1")
	w := invoke("POST", "preview-secret", recipe, chat)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal("invalid response")
	}
	for key, want := range map[string]float64{"counted_input_tokens": 10, "candidates": 2, "input_reservation_tokens": 20, "output_reservation_tokens": 8, "remaining_task_tokens": 12, "remaining_task_output_tokens": 4, "effective_context_window_tokens": 32, "context_headroom_tokens": 18} {
		if result[key] != want {
			t.Errorf("%s = %v, want %v", key, result[key], want)
		}
	}
	if result["scope"] != "new-task-before-review" || result["admitted"] != false {
		t.Fatal(result)
	}
	// The reservation uses the same runtime arithmetic, but preview never creates it.
	ledger := filepath.Join(dir, "comparison.sqlite")
	db, err := reserveTokens(ledger, "task", "step", "recipe", tokenBudget{outputLimit: 12, totalLimit: 40, input: 20, output: 8})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(journal.Path() + ".output.sqlite"); !os.IsNotExist(err) {
		t.Fatal("preview created ledger", err)
	}
	if p.recipes.active.Load().Config.Cheap.OutputLimit != 77 {
		t.Fatal("preview changed active recipe")
	}
	for _, tc := range []struct {
		recipe, chat string
		want         int
	}{
		{strings.Replace(recipe, `"max_task_tokens":40`, `"max_task_tokens":27`, 1), chat, 422},
		{strings.Replace(recipe, `"max_task_output_tokens":12`, `"max_task_output_tokens":7`, 1), chat, 422},
		{recipe, strings.Replace(chat, `"max_tokens":4`, `"max_tokens":5`, 1), 400},
		{`{"schema":"bad","secret":"private"}`, chat, 422},
		{recipe, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"private"}}]}]}`, 422},
	} {
		got := invoke("POST", "preview-secret", tc.recipe, tc.chat)
		if got.Code != tc.want {
			t.Errorf("got%d want%d: %s", got.Code, tc.want, got.Body.String())
		}
		if strings.Contains(got.Body.String(), "private") {
			t.Fatal("private input leaked")
		}
	}
	tokenResult = `{"count":31,"max_model_len":32}`
	if w := invoke("POST", "preview-secret", recipe, chat); w.Code != 422 {
		t.Fatal(w.Code)
	}
	tokenResult = `private provider response`
	if w := invoke("POST", "preview-secret", recipe, chat); w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	before := calls
	p.recipes.active.Load().Config.Cheap.Gateway = true
	if w := invoke("POST", "preview-secret", recipe, chat); w.Code != 422 {
		t.Fatal(w.Code)
	}
	if calls != before {
		t.Fatal("gateway preview called provider")
	}
}

func TestRecipePreviewStrictEnvelopeAndUnlimitedTask(t *testing.T) {
	t.Setenv(config.ProxyTokenEnv, "fixture")
	t.Setenv("CONDUCTOR_RECIPE_PREVIEW_ENABLED", "1")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokenize" {
			t.Error("unexpected provider operation")
		}
		fmt.Fprint(w, `{"count":4,"max_model_len":32}`)
	}))
	defer provider.Close()
	cfg := config.Config{Cheap: config.Tier{Name: "cheap", Model: "model", BaseURL: provider.URL + "/v1", Provider: config.ProviderOpenAICompatible, InputCounter: "vllm", Auth: config.AuthPassthrough}}
	p := New(cfg, nil, nil)
	p.EnableRecipeReload("/never-read")
	doc := `{"recipe":{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"model","max_output_tokens":8,"context_window_tokens":32}},"tier":"cheap","chat":{"messages":[{"role":"user","content":"text"}]}}`
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/config/preview", strings.NewReader(body))
		r.Header.Set(config.ProxyTokenHeader, "fixture")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{doc + `{}`, strings.Replace(doc, `"tier":"cheap"`, `"tier":"other"`, 1), strings.Replace(doc, `"tier":"cheap"`, `"task_id":"existing","tier":"cheap"`, 1), `null`, `{`} {
		if w := call(body); w.Code != 400 && w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call(strings.Repeat(" ", 65537)); w.Code != 413 {
		t.Fatal(w.Code)
	}
	w := call(doc)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["remaining_task_tokens"] != nil || result["remaining_task_output_tokens"] != nil {
		t.Fatal("unconfigured task limits must stay unknown/unbounded", result)
	}
	if _, ok := result["remaining_task_tokens"]; !ok {
		t.Fatal("missing explicit null limit")
	}
	cfgBefore := p.recipes.active.Load().Config
	if cfgBefore.Cheap.ContextWindow != 0 || cfgBefore.Cheap.OutputLimit != 0 {
		t.Fatal("preview installed proposed limits")
	}
}
