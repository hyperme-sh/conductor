package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"github.com/hyperme-sh/hypermesh-conductor/internal/metrics"
	"github.com/hyperme-sh/hypermesh-conductor/internal/taskscope"
)

func usageFixture(t *testing.T) (*Proxy, string, taskUsageRequest, *taskscope.Binding) {
	t.Helper()
	j, err := metrics.OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(config.Config{}, nil, j)
	input := taskUsageRequest{Owner: taskscope.Owner{Tenant: uuid.NewString(), Organization: uuid.NewString(), Owner: uuid.NewString()}, Task: uuid.NewString()}
	return p, j.Path() + ".output.sqlite", input, &taskscope.Binding{Owner: input.Owner, Task: input.Task}
}
func TestTaskUsageHoldsAndMeasuredReconciliation(t *testing.T) {
	p, path, input, binding := usageFixture(t)
	db, err := reserveTokens(path, input.Task, "first", "recipe", tokenBudget{scope: binding, outputLimit: 40, totalLimit: 100, input: 20, output: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(mi, mo, hi, ho, remaining int64) {
		t.Helper()
		s, err := readTaskUsage(context.Background(), path, input)
		if err != nil {
			t.Fatal(err)
		}
		if s.MeasuredInput != mi || s.MeasuredOutput != mo || s.HeldInput == nil || *s.HeldInput != hi || s.HeldOutput != ho || *s.RemainingTotal != remaining {
			t.Fatalf("unexpected snapshot: %+v", s)
		}
	}
	check(0, 0, 20, 30, 50)
	req := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(context.Background(), budgetContextKey{}, db))
	// Unknown or incomplete measurements retain the entire reservation.
	p.recordDispatch(req, metrics.StepRecord{TaskID: input.Task, StepID: "first"})
	check(0, 0, 20, 30, 50)
	usedInput, usedOutput := 12, 7
	p.recordDispatch(req, metrics.StepRecord{TaskID: input.Task, StepID: "first", UsageStatus: metrics.StatusMeasured, InputTokens: &usedInput})
	check(0, 0, 20, 30, 50)
	p.recordDispatch(req, metrics.StepRecord{TaskID: input.Task, StepID: "first", UsageStatus: metrics.StatusMeasured, InputTokens: &usedInput, OutputTokens: &usedOutput})
	check(12, 7, 0, 0, 81)
	next, err := reserveTokens(path, input.Task, "second", "recipe", tokenBudget{scope: binding, outputLimit: 40, totalLimit: 100, input: 10, output: 20})
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	check(12, 7, 10, 20, 51)
	// A read does not admit another attempt or release an unknown hold.
	check(12, 7, 10, 20, 51)
	other := input
	other.Owner.Owner = uuid.NewString()
	if _, err := readTaskUsage(context.Background(), path, other); err == nil {
		t.Fatal("cross-owner query succeeded")
	}
	other = input
	other.Tenant = uuid.NewString()
	if _, err := readTaskUsage(context.Background(), path, other); err == nil {
		t.Fatal("cross-tenant query succeeded")
	}
	for _, scope := range []*taskscope.Binding{nil, {Owner: other.Owner, Task: input.Task}, {Owner: input.Owner, Task: uuid.NewString()}} {
		if next, err := reserveTokens(path, input.Task, "third", "recipe", tokenBudget{scope: scope, outputLimit: 40, totalLimit: 100, output: 1}); next != nil || !errors.Is(err, errTaskBudget) {
			t.Fatalf("owner bypass: %v", err)
		}
	}
	check(12, 7, 10, 20, 51)
}
func TestTaskUsageLegacyAndAbsentRemainUnavailable(t *testing.T) {
	_, path, input, binding := usageFixture(t)
	if _, err := readTaskUsage(context.Background(), path, input); err == nil {
		t.Fatal("absent ledger readable")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("query created ledger")
	}
	db, err := reserveTokens(path, input.Task, "legacy", "recipe", tokenBudget{outputLimit: 10, output: 3})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := readTaskUsage(context.Background(), path, input); err == nil {
		t.Fatal("legacy owner inferred")
	}
	if db, err := reserveTokens(path, input.Task, "adopt", "recipe", tokenBudget{scope: binding, outputLimit: 10, output: 1}); db != nil || !errors.Is(err, errTaskBudget) {
		t.Fatal("legacy task adopted", err)
	}
}
func TestTaskUsageOutputOnlyInputUnknown(t *testing.T) {
	_, path, input, binding := usageFixture(t)
	db, err := reserveTokens(path, input.Task, "one", "recipe", tokenBudget{scope: binding, outputLimit: 10, output: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := readTaskUsage(context.Background(), path, input)
	if err != nil {
		t.Fatal(err)
	}
	if s.HeldInput != nil || s.TotalLimit != nil || s.RemainingTotal != nil || *s.RemainingOutput != 7 {
		t.Fatalf("unknown input represented as counted: %+v", s)
	}
}
func TestTaskUsageRouteSeparateAuthority(t *testing.T) {
	p, path, input, binding := usageFixture(t)
	token := "fixture-task-usage-credential-only-00001"
	t.Setenv("CONDUCTOR_TASK_USAGE_TOKEN_TEST", token)
	raw, _ := json.Marshal([]taskUsageBinding{{Owner: input.Owner, TokenEnv: "CONDUCTOR_TASK_USAGE_TOKEN_TEST"}})
	t.Setenv("CONDUCTOR_TASK_USAGE_BINDINGS", string(raw))
	call := func(body string, header, value string) int {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/tasks/usage", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(header, value)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("cacheable response")
		}
		return w.Code
	}
	body, _ := json.Marshal(input)
	if code := call(string(body), "X-Conductor-Task-Usage-Token", token); code != 503 {
		t.Fatal(code)
	}
	t.Setenv("CONDUCTOR_TASK_USAGE_ENABLED", "true")
	for _, header := range []string{"X-Conductor-Recipe-Token", "X-Conductor-Token", "Authorization"} {
		if code := call(string(body), header, token); code != 403 {
			t.Fatal(header, code)
		}
	}
	if code := call(string(body), "X-Conductor-Task-Usage-Token", token); code != 503 {
		t.Fatal(code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("route created ledger")
	}
	db, err := reserveTokens(path, input.Task, "one", "recipe", tokenBudget{scope: binding, outputLimit: 10, output: 3})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := call(string(body), "X-Conductor-Task-Usage-Token", token); code != 200 {
		t.Fatal(code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("query changed ledger")
	}
	if code := call(string(body)+` {}`, "X-Conductor-Task-Usage-Token", token); code != 400 {
		t.Fatal(code)
	}
	input.Owner.Owner = uuid.NewString()
	body, _ = json.Marshal(input)
	if code := call(string(body), "X-Conductor-Task-Usage-Token", token); code != 403 {
		t.Fatal(code)
	}
}

func TestTaskUsageBindingRollsBackRefusedAdmission(t *testing.T) {
	_, path, input, binding := usageFixture(t)
	if db, err := reserveTokens(path, input.Task, "too-large", "recipe", tokenBudget{scope: binding, outputLimit: 10, output: 11}); db != nil || !errors.Is(err, errTaskBudget) {
		t.Fatal(err)
	}
	// The failed reservation must not claim the task for that owner.
	replacement := *binding
	replacement.Owner.Owner = uuid.NewString()
	db, err := reserveTokens(path, input.Task, "valid", "recipe", tokenBudget{scope: &replacement, outputLimit: 10, output: 2})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := readTaskUsage(context.Background(), path, input); err == nil {
		t.Fatal("failed admission retained ownership")
	}
	input.Owner = replacement.Owner
	if _, err := readTaskUsage(context.Background(), path, input); err != nil {
		t.Fatal(err)
	}
	altered := replacement
	altered.Organization = uuid.NewString()
	for _, budget := range []tokenBudget{{scope: &altered, outputLimit: 10, output: 1}, {scope: &replacement, outputLimit: 11, output: 1}} {
		if db, err := reserveTokens(path, input.Task, "changed", "recipe", budget); db != nil || !errors.Is(err, errTaskBudget) {
			t.Fatal("changed scope or limits admitted", err)
		}
	}
}
