// Package codingloop executes selected project edits and isolated named checks.
package codingloop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/accesspreview"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var Err = errors.New("edit/check action unavailable; inspect task before retrying")

type ReadArguments struct {
	Bytes  int64  `json:"bytes"`
	Object string `json:"object_sha256"`
}
type policyKey struct{}
type policyClock struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel context.CancelFunc
}

func policyContext(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	clock := &policyClock{cancel: cancel}
	return context.WithValue(ctx, policyKey{}, clock), func() {
		clock.mu.Lock()
		if clock.timer != nil {
			clock.timer.Stop()
		}
		clock.mu.Unlock()
		cancel()
	}
}
func bindPolicy(ctx context.Context, expires float64) {
	if clock, ok := ctx.Value(policyKey{}).(*policyClock); ok {
		clock.mu.Lock()
		if clock.timer != nil {
			clock.timer.Stop()
		}
		clock.timer = time.AfterFunc(time.Until(time.Unix(0, int64(expires*1e9))), clock.cancel)
		clock.mu.Unlock()
	}
}

type Check = machineingress.CodingCheck
type Options = machineingress.ExecutionOptions
type Loop struct {
	reviewHook func(context.Context, machineingress.CodingTarget, string, string, any, bool) error
	workHook   func(context.Context, *machineingress.ContinueRun, machineingress.CodingTarget, any, func(context.Context) error) error
	checkHook  func(context.Context, Check, []file) (string, bool, error)

	Client  machineingress.Client
	Policy  accesspreview.Client
	Token   string
	Options Options
	Journal interface{ Reserve(string, string) error }
}
type file struct {
	Path    string `json:"path"`
	Hash    string `json:"sha256"`
	Content string `json:"content"`
}
type edit struct {
	Path    string `json:"path"`
	Before  string `json:"before_sha256"`
	Content string `json:"content"`
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return Err
	}
	return nil
}
func (l *Loop) review(ctx context.Context, t machineingress.CodingTarget, operation, target string, args any, read bool) error {
	if l.reviewHook != nil {
		return l.reviewHook(ctx, t, operation, target, args, read)
	}
	raw, _ := json.Marshal(args)
	hash := digest(raw)
	limits := accesspreview.Limits{Bytes: 16384, Seconds: 30}
	if value, ok := args.(ReadArguments); ok {
		limits.Bytes = uint64(value.Bytes)
		if limits.Bytes == 0 {
			limits.Bytes = 1
		}
	}
	if value, ok := args.(accesspreview.WriteArguments); ok {
		limits.Bytes = value.ContentBytes
		if limits.Bytes == 0 {
			limits.Bytes = 1
		}
	}
	rule := accesspreview.Rule{Operation: operation, Target: target, Limits: limits}
	action := accesspreview.Action{Rule: rule, Arguments: hash}
	if !read {
		action.ArgumentsJSON = string(raw)
	}
	envelope := accesspreview.Envelope{Credential: l.Token, Preview: accesspreview.Request{Binding: t.Binding, RequestID: uuid.NewString(), Project: t.Workspace, TemplateVersion: 1, Template: []accesspreview.Rule{rule}, Action: action}}
	raw, _ = json.Marshal(envelope)
	client := l.Policy
	if read {
		client.Endpoint = strings.TrimSuffix(client.Endpoint, "/action-review") + "/read-review"
		r, e := client.AdmitRead(ctx, raw)
		if e != nil || !r.Permitted || !r.DispatchAdmitted || r.Limits.Bytes < limits.Bytes || r.Limits.Seconds == 0 {
			return Err
		}
		bindPolicy(ctx, min(r.Expires, float64(time.Now().UnixNano())/1e9+float64(r.Limits.Seconds)))
		return ctx.Err()
	}
	r, e := client.Admit(ctx, raw, l.Journal)
	if e != nil || !r.Permitted || !r.DispatchAdmitted || r.Limits.Bytes < limits.Bytes || r.Limits.Seconds == 0 {
		return Err
	}
	bindPolicy(ctx, min(r.Expires, float64(time.Now().UnixNano())/1e9+float64(r.Limits.Seconds)))
	return ctx.Err()
}
func (l *Loop) snapshot(ctx context.Context, run *machineingress.ContinueRun, t machineingress.CodingTarget) ([]file, error) {
	if run.Execution == nil || len(run.Execution.Files) < 1 || len(run.Execution.Files) > 12 || len(run.Execution.Checks) < 1 || len(run.Execution.Checks) > 4 || l.Options.UID == 0 {
		return nil, Err
	}
	seen := map[string]bool{}
	total := 0
	files := []file{}
	for _, path := range run.Execution.Files {
		if seen[path] || !filepath.IsLocal(path) || filepath.Clean(path) != path || (path == ".git" || strings.HasPrefix(path, ".git/")) || strings.ContainsAny(path, "\\*?\x00") {
			return nil, Err
		}
		seen[path] = true
		target := filepath.Join(t.Workspace, path)
		readCtx, cancel := policyContext(ctx)
		data, e := reviewedRead(t.Workspace, path, l.Options.UID, func(size int64, mark string) error {
			return l.review(readCtx, t, "read", target, ReadArguments{size, mark}, true)
		}, readCtx)
		cancel()
		if e != nil {
			return nil, e
		}
		if !utf8.Valid(data) {
			return nil, Err
		}
		total += len(data)
		if total > 10000 {
			return nil, Err
		}
		files = append(files, file{path, digest(data), string(data)})
	}
	return files, nil
}
func (l *Loop) Prepare(ctx context.Context, run *machineingress.ContinueRun, t machineingress.CodingTarget) (string, error) {
	for _, name := range run.Execution.Checks {
		if _, e := l.check(name); e != nil {
			return "", e
		}
	}
	files, e := l.snapshot(ctx, run, t)
	if e != nil {
		return "", e
	}
	raw, _ := json.Marshal(files)
	if len(raw) > 16000 {
		return "", Err
	}
	return string(raw), nil
}
func (l *Loop) check(name string) (Check, error) {
	for _, c := range l.Options.Checks {
		if c.Name == name && len(c.Args) <= 32 && len(c.SHA256) == 64 && filepath.IsAbs(c.Executable) {
			return c, nil
		}
	}
	return Check{}, Err
}
func (l *Loop) action(ctx context.Context, run *machineingress.ContinueRun, t machineingress.CodingTarget, binding any, do func(context.Context) error) error {
	if l.workHook != nil {
		return l.workHook(ctx, run, t, binding, do)
	}
	raw, _ := json.Marshal(binding)
	hash := digest(raw)
	id := uuid.NewString()
	if l.Client.Work(ctx, l.Token, t, run, id, hash, "begin") != nil {
		return Err
	}
	actionCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	actionCtx, stopPolicy := policyContext(actionCtx)
	defer stopPolicy()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-actionCtx.Done():
				return
			case <-ticker.C:
				if l.Client.Work(actionCtx, l.Token, t, run, id, hash, "check") != nil {
					cancel()
					return
				}
			}
		}
	}()
	if e := do(actionCtx); e != nil {
		return e
	}
	if actionCtx.Err() != nil {
		return Err
	}
	return l.Client.Work(ctx, l.Token, t, run, id, hash, "complete")
}
func (l *Loop) Finish(ctx context.Context, run *machineingress.ContinueRun, t machineingress.CodingTarget, result machineingress.CodingResult) (machineingress.CodingResult, error) {
	result.CheckOutcome = "needs-input"
	result.WorkspaceSHA = digest(nil)
	var response struct {
		Edits []edit `json:"edits"`
	}
	if decode([]byte(result.Content), &response) != nil || len(response.Edits) > 12 {
		return result, nil
	}
	files, e := l.snapshot(ctx, run, t)
	if e != nil {
		return result, e
	}
	originals := map[string]file{}
	for _, f := range files {
		originals[f.Path] = f
	}
	seen := map[string]bool{}
	total := 0
	for _, change := range response.Edits {
		old, ok := originals[change.Path]
		total += len(change.Content)
		if !ok || seen[change.Path] || change.Before != old.Hash || total > 10000 {
			return result, nil
		}
		seen[change.Path] = true
	}
	for _, change := range response.Edits {
		if change.Content == originals[change.Path].Content {
			continue
		}
		target := filepath.Join(t.Workspace, change.Path)
		e = l.action(ctx, run, t, change, func(ctx context.Context) error {
			return replaceFile(t.Workspace, change.Path, l.Options.UID, change.Before, []byte(change.Content), func(parentHash string) error {
				args := accesspreview.WriteArguments{ContentHash: digest([]byte(change.Content)), ContentBytes: uint64(len(change.Content)), ObjectHash: change.Before, ParentHash: parentHash}
				return l.review(ctx, t, "write", target, args, false)
			}, ctx)
		})
		if e != nil {
			return result, e
		}
	}
	files, e = l.snapshot(ctx, run, t)
	if e != nil {
		return result, e
	}
	raw, _ := json.Marshal(files)
	workspaceHash := digest(raw)
	result.WorkspaceSHA = workspaceHash
	if run.CheckedWorkspace == workspaceHash {
		result.Feedback = "Workspace unchanged; checks were not repeated"
		return result, nil
	}
	for _, name := range run.Execution.Checks {
		check, e := l.check(name)
		if e != nil {
			return result, e
		}
		argv, _ := json.Marshal(check.Args)
		args := accesspreview.CheckArguments{Executable: check.Executable, ExecutableHash: check.SHA256, ArgvHash: digest(argv), WorkspaceHash: workspaceHash, Sandbox: "linux-bwrap-no-network"}
		var output string
		var passed bool
		e = l.action(ctx, run, t, args, func(ctx context.Context) error {
			if e := l.review(ctx, t, "check", name, args, false); e != nil {
				return e
			}
			var err error
			executor := runCheck
			if l.checkHook != nil {
				executor = l.checkHook
			}
			output, passed, err = executor(ctx, check, files)
			return err
		})
		if e != nil {
			return result, e
		}
		if !passed {
			result.CheckOutcome = "check-failed"
			result.Feedback = fmt.Sprintf("Check %s failed:\n%s", name, output)
			if len(result.Feedback) > 4000 {
				result.Feedback = result.Feedback[:4000]
			}
			return result, nil
		}
	}
	fresh, e := l.snapshot(ctx, run, t)
	if e != nil {
		return result, e
	}
	current, _ := json.Marshal(fresh)
	if digest(current) != workspaceHash {
		result.Feedback = "Workspace changed while checks ran"
		return result, nil
	}
	result.CheckOutcome = "passed"
	return result, nil
}
