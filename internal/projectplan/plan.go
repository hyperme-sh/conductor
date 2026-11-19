// Package projectplan previews bounded acceptance units without dispatching work.
package projectplan

import (
	"errors"
	"github.com/hyperme-sh/hypermesh-conductor/internal/agentadvice"
	"regexp"
)

type Settings struct {
	Style      string `json:"style"`
	WIP        int    `json:"wip"`
	Completion string `json:"completion"`
	Stop       string `json:"stop"`
}
type Unit struct {
	ID         string   `json:"id"`
	Issue      string   `json:"issue"`
	Outcome    string   `json:"outcome"`
	Acceptance string   `json:"acceptance"`
	Scope      string   `json:"scope"`
	Prompt     string   `json:"prompt"`
	Depends    []string `json:"depends_on"`
}
type Request struct {
	Advice   *agentadvice.Request `json:"advice,omitempty"`
	Settings Settings             `json:"settings"`
	Units    []Unit               `json:"units"`
}
type Budget struct {
	Capacity int    `json:"capacity"`
	Input    int    `json:"input"`
	Tools    int    `json:"tools"`
	Checks   int    `json:"checks"`
	Handoff  int    `json:"handoff"`
	Output   int    `json:"output"`
	Source   string `json:"source"`
	Model    string `json:"model"`
}
type Chunk struct {
	Unit
	Assignment     string `json:"assignment_id"`
	EstimatedInput int    `json:"estimated_input"`
	Ready          bool   `json:"ready"`
}
type Preview struct {
	Settings   Settings `json:"settings"`
	Budget     Budget   `json:"budget"`
	Chunks     []Chunk  `json:"chunks"`
	Issues     []string `json:"issues"`
	NeedsInput []string `json:"needs_input"`
	Guide      string   `json:"guide"`
}

var invalid = errors.New("bounded acceptance units and project settings required")
var issue = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/issues/[1-9][0-9]*$`)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func Defaults(style string) (Settings, error) {
	s := Settings{Style: style, WIP: 1, Completion: "Declared acceptance checked", Stop: "Stop on failed check, unknown result, scope change or budget"}
	switch style {
	case "kanban":
		s.Completion = "Selected issue acceptance checked"
	case "vertical-slices":
		s.Completion = "One user-visible outcome checked"
	case "milestones":
		s.Completion = "Declared milestone acceptance checked"
	case "spike":
		s.Completion = "Answer the declared question and record the output"
		s.Stop = "Stop at the declared output or time limit; do not start implementation"
	default:
		return Settings{}, invalid
	}
	return s, nil
}
func text(v string, n int) bool { return len(v) > 0 && len(v) <= n }
func Build(r Request, assignment, model string, capacity, output int) (Preview, error) {
	p := Preview{Settings: r.Settings, Chunks: []Chunk{}, Issues: []string{}, NeedsInput: []string{}, Guide: "Use the project's existing central tracker first. GitHub Issues/Projects fit this repository; Linear or GitLab fit organizations already using them but need an adapter. No new issues are created. Guide: https://github.com/hyperme-sh/hypermesh-docs/blob/main/design/agent-project-playbooks.md"}
	if _, e := Defaults(r.Settings.Style); e != nil || r.Settings.WIP < 1 || r.Settings.WIP > 16 || !text(r.Settings.Completion, 2048) || !text(r.Settings.Stop, 2048) || len(r.Units) < 1 || len(r.Units) > 16 || !text(assignment, 64) || output < 1 || output > 32768 {
		return p, invalid
	}
	source := "configured-model-limit"
	if capacity <= 0 {
		capacity = 8192
		source = "unknown-conservative"
	}
	if capacity > 1048576 {
		capacity = 1048576
	}
	// Reserve tool, check and handoff space before accepting prompt bytes. UTF-8
	// bytes are a conservative token estimate; no arbitrary text cutting occurs.
	tools, checks, handoff := capacity/5, capacity/8, capacity/8
	input := capacity - tools - checks - handoff - output
	p.Budget = Budget{capacity, input, tools, checks, handoff, output, source, model}
	seen := map[string]bool{}
	issues := map[string]bool{}
	ready := 0
	for _, u := range r.Units {
		if !identifier.MatchString(u.ID) || seen[u.ID] || (!issue.MatchString(u.Issue) || len(u.Issue) > 512) || !text(u.Outcome, 256) || !text(u.Acceptance, 512) || !text(u.Scope, 512) || !text(u.Prompt, 32000) || len(u.Depends) > 16 {
			return p, invalid
		}
		deps := map[string]bool{}
		for _, d := range u.Depends {
			if !seen[d] || deps[d] {
				return p, invalid
			}
			deps[d] = true
		}
		seen[u.ID] = true
		if !issues[u.Issue] {
			issues[u.Issue] = true
			p.Issues = append(p.Issues, u.Issue)
		}
		prompt := "Outcome: " + u.Outcome + "\nAcceptance: " + u.Acceptance + "\nScope: " + u.Scope + "\nCompletion: " + r.Settings.Completion + "\nStop: " + r.Settings.Stop + "\n\n" + u.Prompt
		if r.Settings.Style == "spike" {
			prompt += "\nReturn the declared research output only; do not start implementation."
		}
		u.Prompt = prompt
		if len(prompt) > input || len(prompt) > 32000 {
			p.NeedsInput = append(p.NeedsInput, u.ID+": split into smaller acceptance units before dispatch")
		}
		runnable := len(u.Depends) == 0 && ready < r.Settings.WIP && len(prompt) <= input && len(prompt) <= 32000
		if runnable {
			ready++
		}
		p.Chunks = append(p.Chunks, Chunk{u, assignment, len(prompt), runnable})
	}
	return p, nil
}
