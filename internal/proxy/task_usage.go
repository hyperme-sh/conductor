package proxy

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hyperme-sh/hypermesh-conductor/internal/taskscope"
)

const taskUsageSchema = "hypermesh.conductor.task-usage.v1"

var taskUsageTokenName = regexp.MustCompile(`^CONDUCTOR_TASK_USAGE_TOKEN_[A-Z0-9_]{1,64}$`)
var errTaskUsage = errors.New("task usage unavailable")

type taskUsageBinding struct {
	taskscope.Owner
	TokenEnv string `json:"token_env"`
}
type taskUsageRequest struct {
	taskscope.Owner
	Task string `json:"task_id"`
}
type taskUsageResult struct {
	Schema  string             `json:"schema"`
	Outcome string             `json:"outcome"`
	Usage   *taskUsageSnapshot `json:"usage,omitempty"`
}
type taskUsageSnapshot struct {
	MeasuredInput    int64  `json:"measured_input_tokens"`
	MeasuredOutput   int64  `json:"measured_output_tokens"`
	HeldInput        *int64 `json:"held_input_tokens"`
	HeldOutput       int64  `json:"held_output_tokens"`
	MeasuredAttempts int64  `json:"measured_attempts"`
	HeldAttempts     int64  `json:"held_attempts"`
	OutputLimit      *int64 `json:"output_limit"`
	TotalLimit       *int64 `json:"total_limit"`
	RemainingOutput  *int64 `json:"remaining_output_tokens"`
	RemainingTotal   *int64 `json:"remaining_total_tokens"`
}

func taskUsageCredential(token string) (taskscope.Owner, bool) {
	raw := os.Getenv("CONDUCTOR_TASK_USAGE_BINDINGS")
	if len(raw) > 65536 {
		return taskscope.Owner{}, false
	}
	var bindings []taskUsageBinding
	if decodeDraft([]byte(raw), &bindings) != nil || len(bindings) == 0 || len(bindings) > 64 {
		return taskscope.Owner{}, false
	}
	secrets := map[string]bool{}
	owners := map[taskscope.Owner]bool{}
	var scope taskscope.Owner
	matches := 0
	for _, b := range bindings {
		secret := os.Getenv(b.TokenEnv)
		if !b.Owner.Valid() || !taskUsageTokenName.MatchString(b.TokenEnv) || len(secret) < 32 || len(secret) > 4096 || strings.IndexFunc(secret, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || secrets[secret] || owners[b.Owner] {
			return taskscope.Owner{}, false
		}
		secrets[secret] = true
		owners[b.Owner] = true
		if subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1 {
			scope = b.Owner
			matches++
		}
	}
	return scope, matches == 1
}

func (p *Proxy) serveTaskUsage(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
	w.Header().Set("Cache-Control", "private, no-store")
	refuse := func(status int) {
		closeNetworkConn(w)
		writeJSON(w, status, taskUsageResult{Schema: taskUsageSchema, Outcome: "unavailable"})
	}
	if os.Getenv("CONDUCTOR_TASK_USAGE_ENABLED") != "true" {
		refuse(503)
		return
	}
	if r.Method != "POST" {
		refuse(405)
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		refuse(415)
		return
	}
	scope, ok := taskUsageCredential(r.Header.Get("X-Conductor-Task-Usage-Token"))
	if !ok {
		refuse(403)
		return
	}
	if !promoteNetworkConn(w, r) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		refuse(413)
		return
	}
	var input taskUsageRequest
	if decodeDraft(data, &input) != nil || !input.Owner.Valid() || !taskscope.ValidID(input.Task) {
		refuse(400)
		return
	}
	if input.Owner != scope {
		refuse(403)
		return
	}
	if p.Journal == nil {
		refuse(503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	snapshot, err := readTaskUsage(ctx, p.Journal.Path()+".output.sqlite", input)
	if err != nil {
		refuse(503)
		return
	}
	writeJSON(w, 200, taskUsageResult{Schema: taskUsageSchema, Outcome: "available", Usage: &snapshot})
}

// Query only the existing ledger. No creation, schema upgrade or admission runs
// here. The owner lookup and aggregate share one read transaction.
func readTaskUsage(ctx context.Context, path string, input taskUsageRequest) (taskUsageSnapshot, error) {
	var result taskUsageSnapshot
	if !input.Owner.Valid() || !taskscope.ValidID(input.Task) {
		return result, errTaskUsage
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite", uri.String()+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return result, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var outputLimit, totalLimit int64
	err = tx.QueryRowContext(ctx, `SELECT output_limit,total_limit FROM task_owners WHERE task=? AND tenant=? AND organization=? AND owner=?`, input.Task, input.Tenant, input.Organization, input.Owner.Owner).Scan(&outputLimit, &totalLimit)
	if err != nil {
		return result, errTaskUsage
	}
	var heldInput int64
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE(SUM(CASE WHEN measured=1 THEN input_charge ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN measured=1 THEN charge ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN measured=0 THEN input_charge ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN measured=0 THEN charge ELSE 0 END),0),
 COALESCE(SUM(measured),0),COALESCE(SUM(1-measured),0)
 FROM output_attempts WHERE task=?`, input.Task).Scan(&result.MeasuredInput, &result.MeasuredOutput, &heldInput, &result.HeldOutput, &result.MeasuredAttempts, &result.HeldAttempts)
	if err != nil {
		return result, err
	}
	// Fixed-size JSON remains exactly representable by browser consumers. Refuse
	// corrupt/overflowing aggregates rather than returning a plausible balance.
	const maxExact = int64(1<<53 - 1)
	for _, n := range []int64{result.MeasuredInput, result.MeasuredOutput, heldInput, result.HeldOutput, result.MeasuredAttempts, result.HeldAttempts, outputLimit, totalLimit} {
		if n < 0 || n > maxExact/4 {
			return result, errTaskUsage
		}
	}
	if result.MeasuredAttempts+result.HeldAttempts == 0 {
		return result, errTaskUsage
	}
	if totalLimit > 0 || result.HeldAttempts == 0 {
		result.HeldInput = &heldInput
	}
	if outputLimit > 0 {
		remaining := max(int64(0), outputLimit-result.MeasuredOutput-result.HeldOutput)
		result.OutputLimit = &outputLimit
		result.RemainingOutput = &remaining
	}
	if totalLimit > 0 {
		remaining := max(int64(0), totalLimit-result.MeasuredInput-result.MeasuredOutput-heldInput-result.HeldOutput)
		result.TotalLimit = &totalLimit
		result.RemainingTotal = &remaining
	}
	return result, tx.Commit()
}
