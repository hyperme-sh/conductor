//go:build linux

package codingloop

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hyperme-sh/hypermesh-conductor/internal/machineingress"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Loop, *machineingress.ContinueRun, machineingress.CodingTarget, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "file.py")
	if e := os.WriteFile(path, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	uid := uint32(os.Geteuid())
	if uid == 0 {
		uid = 1001
		if e := os.Chown(path, int(uid), -1); e != nil {
			t.Fatal(e)
		}
	}
	l := &Loop{Options: Options{UID: uid, Checks: []Check{{Name: "unit", Executable: "/usr/bin/python3.11", SHA256: digest([]byte("binary"))}}}}
	l.reviewHook = func(context.Context, machineingress.CodingTarget, string, string, any, bool) error { return nil }
	l.workHook = func(ctx context.Context, r *machineingress.ContinueRun, target machineingress.CodingTarget, b any, f func(context.Context) error) error {
		return f(ctx)
	}
	l.checkHook = func(context.Context, Check, []file) (string, bool, error) { return "", true, nil }
	run := &machineingress.ContinueRun{Execution: &machineingress.Execution{Files: []string{"file.py"}, Checks: []string{"unit"}, MaxRepairs: 1}}
	return l, run, machineingress.CodingTarget{Workspace: root}, path
}
func output(content string) machineingress.CodingResult {
	raw, _ := json.Marshal(map[string]any{"edits": []edit{{"file.py", digest([]byte("old")), content}}})
	return machineingress.CodingResult{State: "output-observed", Content: string(raw)}
}
func TestEditsRequireActualCheckResult(t *testing.T) {
	l, run, target, path := fixture(t)
	l.checkHook = func(_ context.Context, _ Check, files []file) (string, bool, error) {
		if files[0].Content != "new" {
			t.Fatal("check did not use edited input")
		}
		return "assertion failed", false, nil
	}
	got, e := l.Finish(context.Background(), run, target, output("new"))
	if e != nil || got.CheckOutcome != "check-failed" || got.Feedback == "" {
		t.Fatalf("%+v %v", got, e)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "new" {
		t.Fatal("edit not retained for repair")
	}
	run.CheckedWorkspace = got.WorkspaceSHA
	l.checkHook = func(context.Context, Check, []file) (string, bool, error) {
		t.Fatal("unchanged checks repeated")
		return "", false, nil
	}
	got, e = l.Finish(context.Background(), run, target, machineingress.CodingResult{Content: `{"edits":[]}`})
	if e != nil || got.CheckOutcome != "needs-input" {
		t.Fatal(got, e)
	}
}
func TestPolicyDenialAndStaleContentNeverWrite(t *testing.T) {
	l, run, target, path := fixture(t)
	l.reviewHook = func(_ context.Context, _ machineingress.CodingTarget, op, _ string, _ any, _ bool) error {
		if op == "write" {
			return Err
		}
		return nil
	}
	if _, e := l.Finish(context.Background(), run, target, output("new")); e == nil {
		t.Fatal("denial ignored")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "old" {
		t.Fatal("denied edit wrote")
	}
	e := replaceFile(target.Workspace, "file.py", l.Options.UID, digest([]byte("old")), []byte("new"), func(string) error { return os.WriteFile(path, []byte("user edit"), 0600) }, context.Background())
	if e == nil {
		t.Fatal("concurrent change ignored")
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != "user edit" {
		t.Fatal("user change lost")
	}
}
func TestNoSymlinkHardlinkTraversalOrRootOwner(t *testing.T) {
	l, _, target, path := fixture(t)
	for _, name := range []string{"../file.py", "/file.py", ".git/config"} {
		if _, e := readFile(target.Workspace, name, l.Options.UID); e == nil {
			t.Fatal(name)
		}
	}
	if e := os.Symlink(path, filepath.Join(target.Workspace, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := readFile(target.Workspace, "link", l.Options.UID); e == nil {
		t.Fatal("symlink")
	}
	if e := os.Link(path, filepath.Join(target.Workspace, "hard")); e != nil {
		t.Fatal(e)
	}
	if _, e := readFile(target.Workspace, "file.py", l.Options.UID); e == nil {
		t.Fatal("hardlink")
	}
	if _, e := readFile(target.Workspace, "file.py", 0); e == nil {
		t.Fatal("root owner")
	}
}
func TestWorkspaceChangedDuringCheckCannotComplete(t *testing.T) {
	l, run, target, path := fixture(t)
	l.checkHook = func(context.Context, Check, []file) (string, bool, error) {
		os.WriteFile(path, []byte("user"), 0600)
		return "pass", true, nil
	}
	got, e := l.Finish(context.Background(), run, target, output("new"))
	if e != nil || got.CheckOutcome == "passed" {
		t.Fatal(got, e)
	}
}

func TestSandboxIsolatesFixture(t *testing.T) {
	if os.Getenv("CONDUCTOR_SANDBOX_CONTRACT") != "1" {
		t.Skip("CI-owned sandbox contract")
	}
	executable, e := filepath.EvalSymlinks("/usr/bin/python3")
	if e != nil {
		t.Fatal(e)
	}
	binary, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private fixture"), 0600)
	t.Setenv("CONDUCTOR_PRIVATE_FIXTURE", "must not cross sandbox")
	code := `import os,socket
assert not os.path.exists(` + fmt.Sprintf("%q", outside) + `)
assert 'CONDUCTOR_PRIVATE_FIXTURE' not in os.environ
assert open('file.py').read() == 'fixture'
try:
 open('/usr/sandbox-write-refused','w')
 raise AssertionError('system filesystem writable')
except OSError: pass
s=socket.socket();s.settimeout(0.2)
try:
 s.connect(('1.1.1.1',443))
 raise AssertionError('network reachable')
except OSError: pass
print('isolated fixture checked')`
	output, passed, e := runCheck(context.Background(), Check{Executable: executable, SHA256: digest(binary), Args: []string{"-c", code}}, []file{{Path: "file.py", Content: "fixture"}})
	if e != nil || !passed {
		t.Fatalf("sandbox: %s %v", output, e)
	}
}

func TestProjectedWorkspaceRefusesBeforeMutation(t *testing.T) {
	l, run, target, path := fixture(t)
	other := filepath.Join(target.Workspace, "other.py")
	os.WriteFile(other, []byte(strings.Repeat("x", 9997)), 0600)
	if os.Geteuid() == 0 {
		os.Chown(other, int(l.Options.UID), -1)
	}
	run.Execution.Files = append(run.Execution.Files, "other.py")
	l.checkHook = func(context.Context, Check, []file) (string, bool, error) {
		t.Fatal("oversized edit checked")
		return "", false, nil
	}
	result, err := l.Finish(t.Context(), run, target, output("longer"))
	if err != nil || result.CheckOutcome != "needs-input" {
		t.Fatal(result, err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "old" {
		t.Fatal("oversized workspace written")
	}
}
func TestWorkExpiryCancelsWhileRenewalStalls(t *testing.T) {
	l, run, target, _ := fixture(t)
	l.workHook = nil
	task := "task"
	run.Current = &task
	renewed := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if b["phase"] == "check" {
			close(renewed)
			<-r.Context().Done()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"admitted": true, "action_id": b["action_id"], "digest": b["digest"], "expires_at": float64(time.Now().Add(1500*time.Millisecond).UnixNano()) / 1e9})
	}))
	defer server.Close()
	l.Client = machineingress.Client{Origin: server.URL, HTTP: server.Client()}
	started := time.Now()
	err := l.action(t.Context(), run, target, "bound action", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatal("expired action continued", err)
	}
	select {
	case <-renewed:
	default:
		t.Fatal("renewal was not pending")
	}
}
