package projectplan

import (
	"strings"
	"testing"
)

func sample() Request {
	s, _ := Defaults("vertical-slices")
	return Request{s, []Unit{{ID: "read", Issue: "https://github.com/a/b/issues/1", Outcome: "Preview", Acceptance: "Preview checked", Scope: "One component", Prompt: "Read the contract"}, {ID: "verify", Issue: "https://github.com/a/b/issues/1", Outcome: "Check", Acceptance: "Check complete", Scope: "One component", Prompt: "Check the contract", Depends: []string{"read"}}}}
}
func TestBoundariesCapacityAndDuplicateIssue(t *testing.T) {
	r := sample()
	p, e := Build(r, "assignment", "model", 4096, 512)
	if e != nil || len(p.Issues) != 1 || !p.Chunks[0].Ready || p.Chunks[1].Ready {
		t.Fatal(p, e)
	}
	r.Units[0].Prompt = strings.Repeat("x", 4000)
	p, e = Build(r, "assignment", "model", 4096, 512)
	if e != nil || len(p.NeedsInput) != 1 || p.Chunks[0].Ready {
		t.Fatal(p, e)
	}
	if len(p.Chunks) != 2 {
		t.Fatal("arbitrary splitting")
	}
}
func TestUnknownWIPAndInvalidDependency(t *testing.T) {
	r := sample()
	r.Units[1].Depends = nil
	p, e := Build(r, "assignment", "", 0, 512)
	if e != nil || p.Budget.Source != "unknown-conservative" || p.Chunks[1].Ready {
		t.Fatal(p, e)
	}
	r.Settings.WIP = 2
	p, e = Build(r, "assignment", "", 0, 512)
	if e != nil || !p.Chunks[1].Ready {
		t.Fatal(p, e)
	}
	r.Units[1].Depends = []string{"missing"}
	if _, e = Build(r, "assignment", "", 0, 512); e == nil {
		t.Fatal("missing dependency")
	}
}
func TestPresetsAndSpikeStops(t *testing.T) {
	for _, style := range []string{"kanban", "vertical-slices", "milestones", "spike"} {
		r := sample()
		r.Settings, _ = Defaults(style)
		p, e := Build(r, "assignment", "model", 8192, 512)
		if e != nil {
			t.Fatal(e)
		}
		if style == "spike" && !strings.Contains(p.Chunks[0].Prompt, "do not start implementation") {
			t.Fatal(p)
		}
	}
}

func TestExpandedPromptDoesNotConsumeWIP(t *testing.T) {
	r := sample()
	r.Units[0].Prompt = strings.Repeat("x", 32000)
	r.Units[1].Depends = nil
	p, e := Build(r, "assignment", "model", 131072, 512)
	if e != nil || len(p.NeedsInput) != 1 || p.Chunks[0].Ready || !p.Chunks[1].Ready {
		t.Fatal(p, e)
	}
}
