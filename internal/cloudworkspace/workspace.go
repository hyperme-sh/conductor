// Package cloudworkspace serves one placement-authorized check on a private worker socket.
package cloudworkspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const Path = "/v1/workspace-task"
const maxLease = 15 * time.Minute

var refused = errors.New("workspace lease unavailable or changed")

type Scope struct {
	Device     uuid.UUID `json:"device_id"`
	Generation uint64    `json:"worker_generation"`
	Task       uuid.UUID `json:"task_id"`
	Lease      uuid.UUID `json:"lease_id"`
	Workspace  string    `json:"workspace"`
}

type Grant struct {
	Version uint8 `json:"version"`
	Scope
	Expires int64  `json:"expires_at_ms"`
	Tool    string `json:"tool"`
	Target  string `json:"target"`
}

type Request struct {
	Scope
	Operation string `json:"operation"`
	Cursor    int    `json:"cursor,omitempty"`
}

type view struct {
	Grant
	State  string `json:"state"`
	Output string `json:"output"`
	Cursor int    `json:"cursor"`
}

type run struct {
	grant Grant
	state string
	log   string
}

type Handler struct {
	read  func() (Grant, error)
	check func(Grant) error
	now   func() time.Time
	mu    sync.Mutex
	run   *run
}

func New(path string) (*Handler, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, refused
	}
	return &Handler{
		read: func() (Grant, error) {
			file, err := openGrant(path)
			if err != nil {
				return Grant{}, err
			}
			defer file.Close()
			var grant Grant
			err = decode(io.LimitReader(file, 4097), &grant)
			return grant, err
		},
		check: checkJSON,
		now:   time.Now,
	}, nil
}

func decode(input io.Reader, value any) error {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return refused
	}
	return nil
}

func (h *Handler) current() (Grant, error) {
	grant, err := h.read()
	now := h.now().UnixMilli()
	if err != nil || grant.Version != 1 || grant.Device == uuid.Nil || grant.Task == uuid.Nil ||
		grant.Lease == uuid.Nil || grant.Generation == 0 || grant.Expires <= now ||
		grant.Expires-now > maxLease.Milliseconds() || !filepath.IsAbs(grant.Workspace) ||
		filepath.Clean(grant.Workspace) != grant.Workspace || grant.Workspace == "/" ||
		len(grant.Workspace) > 4096 || strings.ContainsAny(grant.Workspace, "\r\n\x00") ||
		grant.Tool != "check-json" || !validTarget(grant.Target) {
		return Grant{}, refused
	}
	return grant, nil
}

func validTarget(target string) bool {
	if len(target) <= len(".json") || len(target) > 128 || !strings.HasSuffix(target, ".json") {
		return false
	}
	for _, c := range target {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, refused.Error(), http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		http.Error(w, refused.Error(), http.StatusForbidden)
		return
	}
	var request Request
	if decode(http.MaxBytesReader(w, r.Body, 8192), &request) != nil ||
		(request.Operation != "inspect" && request.Operation != "read" && request.Operation != "invoke") ||
		request.Cursor < 0 || (request.Operation != "read" && request.Cursor != 0) {
		http.Error(w, "invalid workspace task request", http.StatusBadRequest)
		return
	}
	result, err := h.exchange(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func sameOperation(a, b Grant) bool {
	return a.Scope == b.Scope && a.Tool == b.Tool && a.Target == b.Target
}

func (h *Handler) exchange(request Request) (view, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	grant, err := h.current()
	if err != nil || grant.Scope != request.Scope {
		return view{}, refused
	}
	if h.run != nil && !sameOperation(h.run.grant, grant) {
		// The issuer advances generation or creates a different task or lease.
		// Mutation of the admitted tool or target under the same lease fails closed.
		if h.run.grant.Lease == grant.Lease || grant.Generation < h.run.grant.Generation ||
			h.run.state == "running" {
			return view{}, refused
		}
		h.run = nil
	}
	if request.Operation == "invoke" && h.run == nil {
		active := &run{grant: grant, state: "running", log: fmt.Sprintf("Checking %s\n", grant.Target)}
		h.run = active
		go h.execute(active)
	}
	result := view{Grant: grant, State: "ready"}
	if h.run != nil {
		result.State = h.run.state
	}
	if request.Operation == "read" {
		var output string
		if h.run != nil {
			output = h.run.log
		}
		if request.Cursor > len(output) {
			return view{}, errors.New("invalid workspace output cursor")
		}
		result.Output = output[request.Cursor:]
		result.Cursor = len(output)
	}
	return result, nil
}

func (h *Handler) execute(active *run) {
	grant, err := h.current()
	ok := err == nil && sameOperation(grant, active.grant)
	var checkErr error
	if ok {
		checkErr = h.check(grant)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	current, err := h.current()
	if !ok || err != nil || !sameOperation(current, active.grant) {
		active.state, active.log = "revoked", ""
		return
	}
	if checkErr != nil {
		active.state = "failed"
		active.log += "Declared file could not be validated as JSON.\n"
		return
	}
	active.state = "completed"
	active.log += "Declared JSON file is valid.\n"
}
