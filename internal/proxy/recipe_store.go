package proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

const draftSchema = "hypermesh.conductor.recipe-draft.v1"

type draftScope struct {
	Tenant       string `json:"tenant_id"`
	Organization string `json:"organization_id"`
	Subject      string `json:"subject"`
}
type draftBinding struct {
	draftScope
	TokenEnv string `json:"token_env"`
}
type draftRequest struct {
	draftScope
	Action   string          `json:"action"`
	Expected json.RawMessage `json:"expected_revision"`
	Recipe   *string         `json:"recipe"`
}
type draftResult struct {
	Schema   string  `json:"schema"`
	Outcome  string  `json:"outcome"`
	Revision *string `json:"revision"`
	Recipe   *string `json:"recipe"`
	Field    string  `json:"field,omitempty"`
}

var draftTokenName = regexp.MustCompile(`^CONDUCTOR_RECIPE_TOKEN_[A-Z0-9_]{1,64}$`)
var draftUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validDraftScope(s draftScope) bool {
	return draftUUID.MatchString(s.Tenant) && draftUUID.MatchString(s.Organization) && len(s.Subject) > 0 && len(s.Subject) <= 512
}
func decodeDraft(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func draftCredentialScope(token string) (draftScope, bool) {
	raw := os.Getenv("CONDUCTOR_RECIPE_BINDINGS")
	if len(raw) > 65536 {
		return draftScope{}, false
	}
	var bindings []draftBinding
	if decodeDraft([]byte(raw), &bindings) != nil || len(bindings) == 0 || len(bindings) > 64 {
		return draftScope{}, false
	}
	seen := map[string]bool{}
	scopes := map[draftScope]bool{}
	matches := 0
	var scope draftScope
	for _, b := range bindings {
		secret := os.Getenv(b.TokenEnv)
		if !validDraftScope(b.draftScope) || !draftTokenName.MatchString(b.TokenEnv) || len(secret) < 32 || len(secret) > 4096 || strings.IndexFunc(secret, func(r rune) bool { return r < 33 || r > 126 }) >= 0 || seen[secret] || scopes[b.draftScope] {
			return draftScope{}, false
		}
		seen[secret] = true
		scopes[b.draftScope] = true
		if subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1 {
			matches++
			scope = b.draftScope
		}
	}
	return scope, matches == 1
}

// This route never falls back to the general proxy credential or changes active config.
func (p *Proxy) serveDraft(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
	w.Header().Set("Cache-Control", "private, no-store")
	result := draftResult{Schema: draftSchema, Outcome: "unavailable"}
	refuse := func(status int) {
		closeNetworkConn(w)
		writeJSON(w, status, draftResult{Schema: draftSchema, Outcome: "unavailable"})
	}
	if os.Getenv("CONDUCTOR_RECIPE_STORE_ENABLED") != "true" {
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
	scope, authorized := draftCredentialScope(r.Header.Get("X-Conductor-Recipe-Token"))
	if !authorized {
		refuse(403)
		return
	}
	if !promoteNetworkConn(w, r) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 262144))
	if err != nil {
		refuse(413)
		return
	}
	var input draftRequest
	if decodeDraft(data, &input) != nil || !validDraftScope(input.draftScope) {
		refuse(400)
		return
	}
	if input.draftScope != scope {
		refuse(403)
		return
	}
	var expected *string
	switch input.Action {
	case "read":
		if input.Expected != nil || input.Recipe != nil {
			refuse(400)
			return
		}
	case "save":
		if input.Expected == nil || json.Unmarshal(input.Expected, &expected) != nil || expected != nil && !draftUUID.MatchString(*expected) || input.Recipe == nil || len(*input.Recipe) > 65536 {
			refuse(400)
			return
		}
	default:
		refuse(400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cfg := p.Config
	if p.recipes != nil {
		cfg = p.recipes.active.Load().Config
	}
	if input.Action == "save" {
		validation := config.ValidateRecipe([]byte(*input.Recipe), cfg)
		if !validation.Valid {
			result.Outcome = "rejected"
			result.Field = validation.Field
			writeJSON(w, 422, result)
			return
		}
	}
	db, err := openDraftStore(os.Getenv("CONDUCTOR_RECIPE_STORE_DIR"))
	if err != nil {
		refuse(503)
		return
	}
	defer db.Close()
	result, err = accessDraft(ctx, db, input, expected)
	if err != nil {
		refuse(503)
		return
	}
	status := 200
	if result.Outcome == "conflict" {
		status = 409
	}
	writeJSON(w, status, result)
}

func openDraftStore(directory string) (*sql.DB, error) {
	unavailable := errors.New("private draft directory required")
	if !filepath.IsAbs(directory) {
		return nil, unavailable
	}
	clean := filepath.Clean(directory)
	actual, err := filepath.EvalSymlinks(clean)
	if err != nil || actual != clean {
		return nil, unavailable
	}
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, unavailable
	}
	path := filepath.Join(clean, "recipes.sqlite")
	if info, err = os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, unavailable
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", uri.String()+"?_pragma=busy_timeout(3000)&_txlock=immediate")
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

// SQLite serializes conditional updates across processes; read and comparison share a transaction.
func accessDraft(ctx context.Context, db *sql.DB, input draftRequest, expected *string) (draftResult, error) {
	result := draftResult{Schema: draftSchema, Outcome: "loaded"}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recipe_drafts(tenant TEXT NOT NULL, organization TEXT NOT NULL, revision TEXT NOT NULL, recipe TEXT NOT NULL, PRIMARY KEY(tenant,organization)) WITHOUT ROWID`); err != nil {
		return result, err
	}
	var revision, recipe string
	err = tx.QueryRowContext(ctx, "SELECT revision,recipe FROM recipe_drafts WHERE tenant=? AND organization=?", input.Tenant, input.Organization).Scan(&revision, &recipe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	exists := err == nil
	if input.Action == "read" {
		if exists {
			result.Revision = &revision
			result.Recipe = &recipe
		}
		return result, tx.Commit()
	}
	if (expected == nil && exists) || (expected != nil && (!exists || *expected != revision)) {
		result.Outcome = "conflict"
		return result, nil
	}
	revision = uuid.NewString()
	recipe = *input.Recipe
	_, err = tx.ExecContext(ctx, `INSERT INTO recipe_drafts VALUES(?,?,?,?) ON CONFLICT(tenant,organization) DO UPDATE SET revision=excluded.revision,recipe=excluded.recipe`, input.Tenant, input.Organization, revision, recipe)
	if err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.Outcome = "saved"
	result.Revision = &revision
	return result, nil
}
