package proxy

import (
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilities"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelViewContract(t *testing.T) {
	t.Setenv(config.ProxyTokenEnv, "fixture-token")
	tier := config.Tier{Name: "cheap", Model: "fixture", Provider: "openai-compatible", RuntimeRevision: "runtime-1", BaseURL: "http://private-endpoint", APIKeyEnv: "PRIVATE_KEY", ContextWindow: 4096,
		Capabilities: &capabilities.Record{Model: "fixture", Runtime: "runtime-1", Protocol: "openai-compatible", Local: true, Features: map[string]string{"chat": "verified", "tools": "unsupported"}}}
	p := New(config.Config{Cheap: tier}, nil, nil)
	p.EnableRecipeReload("")
	read := func(method, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/config/models", nil)
		r.Header.Set(config.ProxyTokenHeader, token)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	for _, token := range []string{"", "wrong"} {
		w := read("GET", token)
		if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Header())
		}
	}
	if w := read("POST", "fixture-token"); w.Code != 405 {
		t.Fatal(w.Code)
	}
	w := read("GET", "fixture-token")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	for _, private := range []string{"private-endpoint", "PRIVATE_KEY", "fixture-token"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private data exposed")
		}
	}
	var data struct {
		Schema string
		Tiers  map[string]struct {
			Model    string
			Runtime  string `json:"runtime_revision"`
			Local    bool
			Context  int `json:"configured_context_window_tokens"`
			Features map[string]string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	row := data.Tiers["cheap"]
	if data.Schema != "hypermesh.conductor.models.v1" || len(data.Tiers) != 1 || row.Model != "fixture" || row.Runtime != "runtime-1" || !row.Local || row.Context != 4096 || len(row.Features) != 7 || row.Features["chat"] != "verified" || row.Features["tools"] != "unsupported" || row.Features["image"] != "unknown" {
		t.Fatalf("unexpected view: %+v", data)
	}
}
func TestModelViewRefusesUnboundRecords(t *testing.T) {
	for _, record := range []*capabilities.Record{nil, {Model: "other", Runtime: "r1", Protocol: "openai-compatible"}, {Model: "model", Runtime: "old", Protocol: "openai-compatible"}, {Model: "model", Runtime: "r1", Protocol: "other"}, {Model: "model", Runtime: "r1", Protocol: "openai-compatible", Features: map[string]string{"tools": "assumed"}}} {
		cfg := config.Config{Cheap: config.Tier{Name: "cheap", Model: "model", RuntimeRevision: "r1", Provider: "openai-compatible", BaseURL: "http://endpoint", Capabilities: record}}
		if len(modelView(cfg)["tiers"].(map[string]any)) != 0 {
			t.Fatal("unbound record exposed")
		}
	}
}

func TestModelViewBoundsLabels(t *testing.T) {
	for _, value := range []string{"", "  ", "secret\nline", "hidden\u202e", strings.Repeat("x", 1025)} {
		if modelLabel(value) {
			t.Fatal("invalid label accepted")
		}
	}
	if !modelLabel("runtime/model-v1") {
		t.Fatal("valid label rejected")
	}
}
