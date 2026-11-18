// Package agentadvice produces bounded, explainable suggestions without effects.
package agentadvice

import (
	"errors"
	"sort"
	"time"
)

type Thresholds struct {
	MaxAge         int64 `json:"max_age_seconds"`
	Repeated       int   `json:"repeated_failures"`
	Corrections    int   `json:"corrections"`
	Drift          int   `json:"scope_drift"`
	ContextPercent int   `json:"context_percent"`
}

func Defaults() Thresholds { return Thresholds{300, 3, 3, 2, 90} }

type Observations struct {
	At               int64    `json:"observed_at"`
	Failed           []string `json:"failed_approaches"`
	Corrections      *int     `json:"corrections"`
	StaleAssumptions *int     `json:"stale_assumptions"`
	ToolFailures     *int     `json:"tool_failures"`
	ScopeDrift       *int     `json:"scope_drift"`
	OwnsTask         *bool    `json:"owns_task"`
	ContextUsed      *int     `json:"context_used"`
	ContextCapacity  *int     `json:"context_capacity"`
	CostMicros       *int     `json:"cost_micros"`
	Files            []string `json:"files"`
	Blockers         []string `json:"blockers"`
	Next             string   `json:"next_step"`
}
type Request struct {
	Task         string        `json:"task_id"`
	Goal         string        `json:"goal"`
	Constraints  []string      `json:"constraints"`
	State        string        `json:"state"`
	Completed    int           `json:"completed"`
	Total        int           `json:"total"`
	Elapsed      int64         `json:"elapsed_seconds"`
	Checks       []string      `json:"checks"`
	Links        []string      `json:"links"`
	Observations *Observations `json:"observations"`
	Thresholds   *Thresholds   `json:"thresholds"`
}
type Handoff struct {
	Goal        string   `json:"goal"`
	Constraints []string `json:"constraints"`
	Completed   int      `json:"completed"`
	Files       []string `json:"files"`
	Links       []string `json:"links"`
	Checks      []string `json:"checks"`
	Failed      []string `json:"failed_approaches"`
	Blockers    []string `json:"blockers"`
	Next        string   `json:"next_step"`
}
type Advice struct {
	Action       string     `json:"action"`
	Reasons      []string   `json:"reasons"`
	Contributing []string   `json:"contributing"`
	Confidence   string     `json:"confidence"`
	Unknown      []string   `json:"unknown"`
	Next         string     `json:"next_action"`
	Handoff      Handoff    `json:"handoff"`
	Defaults     Thresholds `json:"thresholds"`
}

func stringsBound(v []string, n, limit int) bool {
	if len(v) > n {
		return false
	}
	for _, s := range v {
		if len(s) == 0 || len(s) > limit {
			return false
		}
	}
	return true
}
func Recommend(r Request, now time.Time) (Advice, error) {
	a := Advice{Action: "continue", Reasons: []string{"bounded-progress"}, Contributing: []string{}, Confidence: "low", Unknown: []string{}, Next: "Continue the declared next chunk within current limits", Defaults: Defaults(), Handoff: Handoff{Goal: r.Goal, Constraints: r.Constraints, Completed: r.Completed, Links: r.Links, Checks: r.Checks, Files: []string{}, Failed: []string{}, Blockers: []string{}, Next: "Refresh current owner status before acting"}}
	bad := errors.New("bounded task observations required")
	if len(r.Task) == 0 || len(r.Task) > 64 || len(r.Goal) > 4096 || r.Completed < 0 || r.Total < 1 || r.Total > 16 || r.Completed > r.Total || r.Elapsed < 0 || r.Elapsed > 86400 || !stringsBound(r.Constraints, 16, 4096) || !stringsBound(r.Checks, 16, 256) || !stringsBound(r.Links, 12, 1024) {
		return a, bad
	}
	if r.Thresholds != nil {
		a.Defaults = *r.Thresholds
	}
	t := a.Defaults
	if t.MaxAge < 1 || t.MaxAge > 3600 || t.Repeated < 2 || t.Repeated > 32 || t.Corrections < 1 || t.Corrections > 100 || t.Drift < 1 || t.Drift > 100 || t.ContextPercent < 50 || t.ContextPercent > 99 {
		return a, bad
	}
	set := func(action, reason, next string) { a.Action = action; a.Reasons = []string{reason}; a.Next = next }
	if r.State == "paused" || r.State == "needs-input" || r.State == "external-blocker" || r.State == "limit" {
		a.Next = "Keep the current stop state until its declared reason is resolved"
	}
	if r.State == "completed" && r.Completed == r.Total {
		set("retire", "task-complete", "Confirm completion and release this worker while preserving task history")
		a.Confidence = "high"
		a.Handoff.Next = a.Next
		return a, nil
	}
	o := r.Observations
	if o == nil || o.At > now.Unix() || now.Unix()-o.At > t.MaxAge {
		a.Unknown = append(a.Unknown, "fresh-work-quality", "context", "cost", "ownership")
		a.Reasons = []string{"observations-missing-or-stale"}
		a.Next = "Refresh observations and owner status; do not replace a worker based on stale metrics"
		return a, nil
	}
	if !stringsBound(o.Failed, 32, 128) || !stringsBound(o.Files, 16, 512) || !stringsBound(o.Blockers, 16, 512) || len(o.Next) > 1024 {
		return a, bad
	}
	for _, v := range []*int{o.Corrections, o.StaleAssumptions, o.ToolFailures, o.ScopeDrift, o.ContextUsed, o.ContextCapacity, o.CostMicros} {
		if v != nil && (*v < 0 || *v > 1000000000) {
			return a, bad
		}
	}
	a.Handoff.Files = o.Files
	a.Handoff.Failed = o.Failed
	a.Handoff.Blockers = o.Blockers
	a.Handoff.Next = o.Next
	a.Confidence = "medium"
	for name, v := range map[string]*int{"corrections": o.Corrections, "stale-assumptions": o.StaleAssumptions, "tool-failures": o.ToolFailures, "scope-drift": o.ScopeDrift, "context-used": o.ContextUsed, "context-capacity": o.ContextCapacity, "cost": o.CostMicros} {
		if v == nil {
			a.Unknown = append(a.Unknown, name)
		}
	}
	if o.OwnsTask == nil {
		a.Unknown = append(a.Unknown, "ownership")
	}
	sort.Strings(a.Unknown)
	counts := map[string]int{}
	repeated := false
	for _, id := range o.Failed {
		counts[id]++
		if counts[id] >= t.Repeated {
			repeated = true
		}
	}
	if o.OwnsTask != nil && !*o.OwnsTask {
		set("reassign", "ownership-mismatch", "Confirm the current owner and explicitly select a valid worker; do not broaden scope")
	} else if repeated || (o.Corrections != nil && *o.Corrections >= t.Corrections) || (o.StaleAssumptions != nil && *o.StaleAssumptions >= t.Corrections) || (o.ScopeDrift != nil && *o.ScopeDrift >= t.Drift) {
		set("restart", "strategy-stalled", "Checkpoint failed approaches and constraints, then select a changed strategy before replacement")
	} else if o.ToolFailures != nil && *o.ToolFailures > 0 {
		set("checkpoint-compact", "temporary-tool-failure", "Checkpoint and check tool availability before retrying; do not retire solely for an outage")
	} else if o.ContextUsed != nil && o.ContextCapacity != nil && *o.ContextCapacity > 0 && int64(*o.ContextUsed)*100 >= int64(*o.ContextCapacity)*int64(t.ContextPercent) {
		set("checkpoint-compact", "productive-context-pressure", "Compact completed work and preserve the next acceptance unit; fullness alone does not justify retirement")
	}
	if repeated {
		a.Contributing = append(a.Contributing, "repeated-failed-approach")
	}
	if r.Completed > 0 {
		a.Contributing = append(a.Contributing, "completed-chunks")
	}
	if r.Elapsed > 0 {
		a.Contributing = append(a.Contributing, "elapsed-time-reported")
	}
	if o.CostMicros != nil {
		a.Contributing = append(a.Contributing, "cost-reported")
	}
	return a, nil
}
