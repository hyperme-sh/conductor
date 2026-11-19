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

type fixtureHooks struct {
	prepare func(context.Context, *ContinueRun, CodingTarget) (string, error)
	finish  func(context.Context, *ContinueRun, CodingTarget, CodingResult) (CodingResult, error)
}

func (h fixtureHooks) Prepare(c context.Context, r *ContinueRun, t CodingTarget) (string, error) {
	return h.prepare(c, r, t)
}
func (h fixtureHooks) Finish(c context.Context, r *ContinueRun, t CodingTarget, v CodingResult) (CodingResult, error) {
	return h.finish(c, r, t, v)
}
func TestEditHooksBindContextAndActualCheckResult(t *testing.T) {
	s := submission()
	target := CodingTarget{Binding: s.Binding, Workspace: "/project"}
	chunk, task := uuid.NewString(), uuid.NewString()
	for _, failure := range []string{"", "prepare", "finish"} {
		t.Run(failure, func(t *testing.T) {
			run := ContinueRun{ID: uuid.NewString(), Revision: 1, Generation: 1, Continue: true, State: "running", Workspace: target.Workspace, Assignment: target.Assignment, Execution: &Execution{Files: []string{"file.py"}, Checks: []string{"unit"}, MaxRepairs: 1}}
			json.Unmarshal([]byte(`[{"id":"`+chunk+`","depends_on":[],"state":"pending"}]`), &run.Chunks)
			advances, executions, records := 0, 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				switch {
				case strings.HasSuffix(r.URL.Path, "continue-state"):
				case strings.HasSuffix(r.URL.Path, "continue-advance"):
					advances++
					if body["context"] != "reviewed files" || body["chunk_id"] != chunk {
						t.Error("missing reviewed context")
					}
					run.Current = &task
				case strings.HasSuffix(r.URL.Path, "continue-record"):
					records++
					if body["chunk_id"] != task || body["outcome"] != "check-failed" || body["feedback"] != "actual check failure" || body["workspace_sha256"] != strings.Repeat("b", 64) {
						t.Error("check result not bound", body)
					}
				default:
					t.Error("unexpected endpoint")
				}
				json.NewEncoder(w).Encode(map[string]any{"run": run})
			}))
			defer server.Close()
			hooks := fixtureHooks{prepare: func(context.Context, *ContinueRun, CodingTarget) (string, error) {
				if failure == "prepare" {
					return "", refused
				}
				return "reviewed files", nil
			}, finish: func(_ context.Context, r *ContinueRun, _ CodingTarget, v CodingResult) (CodingResult, error) {
				if failure == "finish" {
					return v, refused
				}
				if v.Content != "model edits" || *r.Current != task {
					t.Error("lost model output binding")
				}
				v.CheckOutcome = "check-failed"
				v.Feedback = "actual check failure"
				v.WorkspaceSHA = strings.Repeat("b", 64)
				return v, nil
			}}
			client := Client{Origin: server.URL, HTTP: server.Client()}
			_, err := client.continueCodingHooks(t.Context(), "hm_msi_fixture", target, func(_ context.Context, expected string, generation int) (CodingResult, error) {
				executions++
				if expected != task || generation != 1 {
					t.Error("wrong worker")
				}
				return CodingResult{State: "output-observed", Task: task, OutputSHA: strings.Repeat("a", 64), Content: "model edits", CheckOutcome: "passed"}, nil
			}, hooks)
			if (err != nil) != (failure != "") {
				t.Fatal(err)
			}
			if failure == "prepare" && (advances != 0 || executions != 0) {
				t.Fatal("dispatched without reviewed context")
			}
			if records != map[bool]int{true: 1, false: 0}[failure == ""] {
				t.Fatal("recorded an unconfirmed check")
			}
		})
	}
}
