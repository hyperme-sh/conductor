package proxy

import (
	"bytes"
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"net/http/httptest"
	"testing"
)

func TestRecipeValidationRoute(t *testing.T) {
	t.Setenv(config.ProxyTokenEnv, "fixture-token")
	p := New(config.Config{Cheap: config.Tier{Name: "cheap", Model: "private-model", OutputLimit: 99}}, nil, nil)
	p.EnableRecipeReload("/must-not-read")
	read := func(method, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/config/validate", bytes.NewBufferString(body))
		r.Header.Set(config.ProxyTokenHeader, token)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	if w := read("POST", "", "{}"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := read("GET", "fixture-token", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := read("POST", "fixture-token", string(bytes.Repeat([]byte(" "), 65537))); w.Code != 413 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"private-model","max_output_tokens":12}}`, `{"schema":"hypermesh.conductor.recipe.v1","cheap":{"model":"wrong","max_output_tokens":12}}`, `{"private":"raw-secret"}`} {
		w := read("POST", "fixture-token", body)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Header())
		}
		var result config.RecipeValidation
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Schema != "hypermesh.conductor.recipe-validation.v1" {
			t.Fatal(result)
		}
		for _, private := range []string{"private-model", "raw-secret", "must-not-read"} {
			if bytes.Contains(w.Body.Bytes(), []byte(private)) {
				t.Fatal("private data exposed")
			}
		}
		if p.recipes.active.Load().Config.Cheap.OutputLimit != 99 {
			t.Fatal("active limits changed")
		}
	}
}
