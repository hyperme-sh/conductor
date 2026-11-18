package agentadvice

import (
	"reflect"
	"testing"
	"time"
)

func ip(n int) *int { return &n }
func fixture() Request {
	return Request{Task: "task", Goal: "Goal", State: "running", Completed: 1, Total: 2, Observations: &Observations{At: 100, ContextUsed: ip(95), ContextCapacity: ip(100), Corrections: ip(0), ToolFailures: ip(0)}}
}
func TestProductiveHighContextAndLowContextLoop(t *testing.T) {
	r := fixture()
	a, e := Recommend(r, time.Unix(101, 0))
	if e != nil || a.Action != "checkpoint-compact" {
		t.Fatal(a, e)
	}
	r.Observations.ContextUsed = ip(1)
	r.Observations.Failed = []string{"same", "same", "same"}
	a, e = Recommend(r, time.Unix(101, 0))
	if e != nil || a.Action != "restart" {
		t.Fatal(a, e)
	}
}
func TestOutageCompletionStaleAndNoMutation(t *testing.T) {
	r := fixture()
	r.Observations.ToolFailures = ip(1)
	before := *r.Observations
	a, e := Recommend(r, time.Unix(101, 0))
	if e != nil || a.Reasons[0] != "temporary-tool-failure" || !reflect.DeepEqual(before, *r.Observations) {
		t.Fatal(a, e)
	}
	a, e = Recommend(r, time.Unix(1000, 0))
	if e != nil || a.Action != "continue" || len(a.Unknown) == 0 {
		t.Fatal(a, e)
	}
	r.State = "completed"
	r.Completed = 2
	a, e = Recommend(r, time.Unix(1000, 0))
	if e != nil || a.Action != "retire" {
		t.Fatal(a, e)
	}
}

func TestTunableThresholdsAndOwnership(t *testing.T) {
	r := fixture()
	no := false
	r.Observations.OwnsTask = &no
	a, e := Recommend(r, time.Unix(101, 0))
	if e != nil || a.Action != "reassign" {
		t.Fatal(a, e)
	}
	r.Observations.OwnsTask = nil
	r.Observations.ContextUsed = ip(1)
	r.Observations.Failed = []string{"same", "same"}
	d := Defaults()
	d.Repeated = 2
	r.Thresholds = &d
	a, e = Recommend(r, time.Unix(101, 0))
	if e != nil || a.Action != "restart" {
		t.Fatal(a, e)
	}
}
