package machineingress

import (
	"bytes"
	"encoding/json"
	"github.com/hyperme-sh/hypermesh-conductor/internal/projectplan"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlanUsesScopedContextWithoutDispatch(t *testing.T) {
	s := submission()
	server, c := authority(t, s, nil)
	defer server.Close()
	calls := 0
	h, _ := New(*c, s.Tenant, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }), "proxy", func() string { return "private" })
	h.ModelCapacity = func(destination string) (string, int) {
		if destination != "local-model" {
			t.Fatal(destination)
		}
		return "selected", 4096
	}
	settings, _ := projectplan.Defaults("spike")
	in := PlanInput{s.Binding, "local-model", 128, projectplan.Request{Settings: settings, Units: []projectplan.Unit{{ID: "one", Issue: "https://github.com/a/b/issues/1", Outcome: "Answer", Acceptance: "Answer checked", Scope: "Read one file", Prompt: "Answer question"}}}}
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest("POST", "/v1/machine/plan", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer hm_msi_test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var p projectplan.Preview
	json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 200 || p.Budget.Model != "selected" || p.Chunks[0].Assignment != s.Assignment || calls != 0 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	in.Binding.Session = "bad"
	raw, _ = json.Marshal(in)
	r = httptest.NewRequest("POST", "/v1/machine/plan", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer hm_msi_test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 || calls != 0 {
		t.Fatal("invalid scope accepted")
	}
}
