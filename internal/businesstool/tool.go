// Package businesstool dispatches bounded, read-only business integrations for
// a trusted runner. Model arguments cannot select credentials or authority.
package businesstool

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilitydispatch"
)

type Status string

func (s Status) Error() string { return string(s) }

const (
	Invalid          Status = "invalid_tool_call"
	Denied           Status = "permission_denied"
	Unavailable      Status = "provider_unavailable"
	ConsentRequired  Status = "consent_required"
	Revoked          Status = "connection_revoked"
	Throttled        Status = "throttled"
	StaleSync        Status = "stale_sync"
	NotFound         Status = "not_found"
	StaleRecord      Status = "stale_record"
	OperationChanged Status = "operation_changed"
)

const (
	maxInput   = 16 << 10
	maxLimit   = 50
	maxQuery   = 200
	maxDraft   = 4000
	maxReply   = 8000
	maxHistory = 30
)

var (
	reference = regexp.MustCompile(`^[A-Za-z0-9._:=+@-]{1,200}$`)
	leadID    = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

type Binding struct {
	TenantID     string  `json:"tenant_id"`
	WorkspaceID  string  `json:"workspace_id"`
	ConnectionID string  `json:"connection_id"`
	GrantID      string  `json:"grant_id"`
	Provider     string  `json:"provider"`
	Target       string  `json:"target"`
	ActorKind    string  `json:"actor_kind"`
	ActorID      string  `json:"actor_id"`
	RunID        *string `json:"run_id"`
}

func isUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func (b Binding) valid() bool {
	if !isUUID(b.TenantID) || !isUUID(b.WorkspaceID) || !isUUID(b.ConnectionID) || !isUUID(b.GrantID) ||
		!reference.MatchString(b.Target) || !reference.MatchString(b.ActorID) {
		return false
	}
	switch b.ActorKind {
	case "person":
		return b.RunID == nil
	case "skill", "schedule":
		return b.RunID != nil && isUUID(*b.RunID)
	default:
		return false
	}
}

type Config struct {
	Version        int      `json:"version"`
	Binding        Binding  `json:"binding"`
	OutlookFixture string   `json:"outlook_fixture,omitempty"`
	OutlookState   string   `json:"outlook_state,omitempty"`
	OutlookFolders []string `json:"outlook_folders,omitempty"`
}

type Call struct {
	Version   int             `json:"version"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type Result struct {
	Version       int    `json:"version"`
	Tool          string `json:"tool"`
	UntrustedData bool   `json:"untrusted_data"`
	Data          any    `json:"data"`
}

type Reviewer interface {
	Authorize(context.Context, Binding, string) error
}

type Runner struct {
	Binding Binding
	Review  Reviewer
	Odoo    *Odoo
	Outlook *Outlook
}

func (r Runner) Run(ctx context.Context, call Call) (Result, error) {
	if r.Review == nil || !r.Binding.valid() || call.Version != 1 || len(call.Arguments) > maxInput {
		return Result{}, Invalid
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var data any
	var err error
	switch r.Binding.Provider {
	case "odoo":
		if r.Odoo == nil {
			return Result{}, Invalid
		}
		data, err = r.runOdoo(ctx, call)
	case "outlook":
		if r.Outlook == nil {
			return Result{}, Invalid
		}
		data, err = r.runOutlook(ctx, call)
	default:
		return Result{}, Invalid
	}
	if err != nil {
		return Result{}, err
	}
	return Result{1, call.Tool, true, data}, nil
}

func (r Runner) authorize(ctx context.Context, operation string) error {
	if ctx.Err() != nil || r.Review.Authorize(ctx, r.Binding, operation) != nil || ctx.Err() != nil {
		return Denied
	}
	return nil
}

func decodeArgument(raw []byte, into any) error {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return Invalid
	}
	if capabilitydispatch.Decode(raw, into) != nil {
		return Invalid
	}
	return nil
}

func ReadCall(in io.Reader) (Call, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxInput+1))
	var call Call
	if err != nil || len(raw) > maxInput || decodeArgument(raw, &call) != nil {
		return Call{}, Invalid
	}
	return call, nil
}

func ReadConfig(path string) (Config, error) {
	var config Config
	raw, err := readOwnedFile(path, maxInput)
	if err != nil || decodeArgument(raw, &config) != nil || config.Version != 1 || !config.Binding.valid() {
		return Config{}, Invalid
	}
	return config, nil
}

func readOwnedFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, Invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Invalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, Invalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, Invalid
	}
	return raw, nil
}

func pageLimit(limit int) (int, error) {
	if limit < 0 || limit > maxLimit {
		return 0, Invalid
	}
	if limit == 0 {
		return 20, nil
	}
	return limit, nil
}

func boundedText(value string, length int) bool {
	return len(value) <= length && !strings.ContainsRune(value, 0)
}
