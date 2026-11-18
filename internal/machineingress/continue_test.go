package machineingress

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContinueUsesOwnerStateAcrossTwoChunksAndStops(t *testing.T) {
	s := submission()
	target := CodingTarget{Binding: s.Binding, Workspace: "/project", Class: s.Class, Destination: s.Destination, MaxOutput: s.MaxOutput}
	first, second := uuid.NewString(), uuid.NewString()
	run := ContinueRun{ID: uuid.NewString(), Revision: 1, Continue: true, State: "running", Workspace: target.Workspace, Assignment: target.Assignment}
	raw := `[{"id":"` + first + `","depends_on":[],"state":"pending"},{"id":"` + second + `","depends_on":["` + first + `"],"state":"pending"}]`
	json.Unmarshal([]byte(raw), &run.Chunks)
	calls, records := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["workspace"] != target.Workspace || body["device_id"] != s.Device || r.Header.Get("Authorization") != "Bearer hm_msi_test" {
			t.Error("scope not bound")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "continue-state"):
		case strings.HasSuffix(r.URL.Path, "continue-advance"):
			if run.Current != nil || int(body["revision"].(float64)) != run.Revision || body["chunk_id"] != NextChunk(run) {
				t.Error("bad advance")
			}
			id := body["chunk_id"].(string)
			run.Current = &id
			run.Revision++
		case strings.HasSuffix(r.URL.Path, "continue-record"):
			records++
			if body["outcome"] != "passed" || body["chunk_id"] != *run.Current {
				t.Error("bad record")
			}
			for i := range run.Chunks {
				if run.Chunks[i].ID == *run.Current {
					run.Chunks[i].State = "passed"
				}
			}
			run.Current = nil
			run.Revision++
			if records == 2 {
				run.State = "completed"
			}
		default:
			t.Error("unexpected endpoint")
		}
		json.NewEncoder(w).Encode(map[string]any{"run": run})
	}))
	defer server.Close()
	c := Client{Origin: server.URL, HTTP: server.Client()}
	run.Generation = 2
	execute := func(_ context.Context, expected string, generation int) (CodingResult, error) {
		calls++
		if expected != *run.Current || generation != run.Generation {
			t.Fatal("selected task not bound before execution")
		}
		return CodingResult{Version: 1, Task: *run.Current, State: "output-observed", OutputSHA: strings.Repeat("a", 64), CheckOutcome: "passed"}, nil
	}
	for i := 0; i < 3; i++ {
		_, e := c.continueCoding(t.Context(), "hm_msi_test", target, execute)
		if e != nil {
			t.Fatal(e)
		}
	}
	if calls != 2 || records != 2 || run.State != "completed" {
		t.Fatal(calls, records, run.State)
	}
	for _, state := range []string{"paused", "limit", "needs-input", "external-blocker"} {
		run.State = state
		result, e := c.continueCoding(t.Context(), "hm_msi_test", target, execute)
		if e != nil || result.State != "idle" || calls != 2 {
			t.Fatal(state, result, e)
		}
	}
}
func TestNextChunkRefusesDependenciesAndRepeatedWork(t *testing.T) {
	run := ContinueRun{State: "running", Continue: true}
	first, second := uuid.NewString(), uuid.NewString()
	json.Unmarshal([]byte(`[{"id":"`+first+`","state":"external-blocker","depends_on":[]},{"id":"`+second+`","state":"pending","depends_on":["`+first+`"]}]`), &run.Chunks)
	if NextChunk(run) != "" {
		t.Fatal("blocked dependency selected")
	}
	run.Chunks[0].State = "passed"
	if NextChunk(run) != second {
		t.Fatal("ready dependency refused")
	}
	run.Continue = false
	if NextChunk(run) != "" {
		t.Fatal("continue off ignored")
	}
	run.Continue = true
	run.Current = &second
	if NextChunk(run) != "" {
		t.Fatal("active work duplicated")
	}
}
