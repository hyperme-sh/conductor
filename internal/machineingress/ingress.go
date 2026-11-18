// Package machineingress binds a single prompt invocation to Plane's current
// enrolled machine and durable assignment. It never accepts renter credentials.
package machineingress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hyperme-sh/hypermesh-conductor/internal/spendlimit"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/operationoutcome"
	"github.com/hyperme-sh/hypermesh-conductor/internal/taskscope"
)

const OperationTimeout = 300 * time.Second

var refused = errors.New("machine operation unavailable or refused")

type Binding struct {
	Enrollment string `json:"enrollment_id,omitempty"`
	Version    int    `json:"version"`
	Tenant     string `json:"tenant_id"`
	Device     string `json:"device_id"`
	Bot        string `json:"bot_id"`
	Assignment string `json:"assignment_id"`
	Session    string `json:"session_id"`
}
type Submission struct {
	Binding
	Task        string `json:"task_id"`
	Operation   string `json:"operation_id"`
	Class       string `json:"class"`
	Destination string `json:"destination"`
	Prompt      string `json:"prompt"`
	MaxOutput   int    `json:"max_output_tokens"`
}
type Fact struct {
	MoneyCurrency    string  `json:"money_currency,omitempty"`
	CallerDevice     string  `json:"caller_device_id,omitempty"`
	CallerEnrollment string  `json:"caller_enrollment_id,omitempty"`
	Version          int     `json:"version"`
	Request          string  `json:"request_id"`
	Tenant           string  `json:"tenant_id"`
	Owner            string  `json:"owner_id"`
	Principal        string  `json:"principal"`
	Organization     string  `json:"organization_id"`
	Device           string  `json:"device_id"`
	Bot              string  `json:"bot_id"`
	Assignment       string  `json:"assignment_id"`
	Session          string  `json:"session_id"`
	Expires          float64 `json:"expires_at"`
	Operation        string  `json:"operation_id,omitempty"`
	Task             string  `json:"task_id,omitempty"`
	Digest           string  `json:"request_sha256,omitempty"`
	State            string  `json:"state,omitempty"`
}
type Client struct {
	Phone  bool
	Origin string
	HTTP   *http.Client
}

func canonicalID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u != uuid.Nil && u.String() == id
}
func (b Binding) Valid() bool {
	if b.Version != 1 {
		return false
	}
	for _, id := range []string{b.Tenant, b.Device, b.Bot, b.Assignment, b.Session} {
		if !canonicalID(id) {
			return false
		}
	}
	return true
}
func ValidOrigin(origin string) bool {
	u, e := url.Parse(origin)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}

// Check authenticates through the existing Plane owner. An absent task requests
// preview context only; it never consumes an operation or authorizes execution.
func (c Client) Check(ctx context.Context, token string, b Binding, task, operation, digest, command string) (Fact, error) {
	return c.check(ctx, token, b, task, operation, digest, command, false)
}

// ActiveContext requires current running consent and a fresh heartbeat.
// It establishes identity only; policy and OS access remain separate checks.
func (c Client) ActiveContext(ctx context.Context, token string, b Binding) (Fact, error) {
	return c.check(ctx, token, b, "", "", "", "", true)
}

func (c Client) check(ctx context.Context, token string, b Binding, task, operation, digest, command string, active bool) (Fact, error) {
	if !ValidOrigin(c.Origin) || !b.Valid() || !strings.HasPrefix(token, "hm_msi_") || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return Fact{}, refused
	}
	if (c.Phone && (!canonicalID(b.Enrollment) || task == "")) || (!c.Phone && b.Enrollment != "") {
		return Fact{}, refused
	}
	nonce := uuid.NewString()
	body := map[string]any{"version": 1, "request_id": nonce, "device_id": b.Device, "assignment_id": b.Assignment, "session_id": b.Session}
	path := "context"
	if active {
		path = "access-context"
	}
	if task != "" {
		if !canonicalID(task) || !canonicalID(operation) || len(digest) != 64 {
			return Fact{}, refused
		}
		body["task_id"] = task
		body["operation_id"] = operation
		body["request_sha256"] = digest
		body["command"] = command
		path = "operation"
	}
	kind := "machine"
	if c.Phone {
		kind = "phone"
		body["enrollment_id"] = b.Enrollment
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Origin, "/")+"/api/v1/hypermesh/bots/"+b.Bot+"/"+kind+"/"+path, bytes.NewReader(raw))
	if e != nil {
		return Fact{}, refused
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", b.Tenant)
	client := http.Client{Timeout: 5 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, e := client.Do(req)
	if e != nil {
		return Fact{}, refused
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Fact{}, refused
	}
	raw, e = io.ReadAll(io.LimitReader(response.Body, 8193))
	if e != nil || len(raw) > 8192 {
		return Fact{}, refused
	}
	var fact Fact
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&fact) != nil || dec.Decode(new(any)) != io.EOF {
		return Fact{}, refused
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if fact.Version != 1 || fact.Request != nonce || fact.Tenant != b.Tenant || fact.Bot != b.Bot || fact.Assignment != b.Assignment || fact.Session != b.Session || !canonicalID(fact.Owner) || !canonicalID(fact.Organization) || fact.Principal == "" || len(fact.Principal) > 512 || fact.Expires <= now || fact.Expires > now+11 {
		return Fact{}, refused
	}
	if c.Phone {
		if !canonicalID(fact.Device) || fact.CallerDevice != b.Device || fact.CallerEnrollment != b.Enrollment {
			return Fact{}, refused
		}
	} else if fact.Device != b.Device || fact.CallerDevice != "" || fact.CallerEnrollment != "" {
		return Fact{}, refused
	}
	if task != "" && (fact.Task != task || fact.Operation != operation || fact.Digest != digest || (command == "begin" || command == "check") && fact.State != "active" || command == "complete" && fact.State != "completed" || command == "cancel" && fact.State != "unknown") {
		return Fact{}, refused
	}
	if task == "" && (fact.Task != "" || fact.Operation != "" || fact.Digest != "" || fact.State != "") {
		return Fact{}, refused
	}
	return fact, nil
}

type Handler struct {
	ModelCapacity func(string) (string, int, int)
	Authenticated func(http.ResponseWriter, *http.Request) bool
	Client        Client
	Tenant        string
	Dispatch      http.Handler
	ProxyHeader   string
	ProxyToken    func() string
	slots         chan struct{}
}

func New(client Client, tenant string, dispatch http.Handler, header string, token func() string) (*Handler, error) {
	if !ValidOrigin(client.Origin) || !canonicalID(tenant) || dispatch == nil || header == "" || token == nil {
		return nil, refused
	}
	return &Handler{Client: client, Tenant: tenant, Dispatch: dispatch, ProxyHeader: header, ProxyToken: token, slots: make(chan struct{}, 8)}, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code int) { w.Header().Set("Connection", "close"); http.Error(w, refused.Error(), code) }
	if r.Method != "POST" {
		fail(405)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		fail(429)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if e != nil {
		fail(400)
		return
	}
	if r.URL.Path == "/v1/machine/plan" && !h.Client.Phone {
		h.plan(w, r, raw)
		return
	}
	var s Submission
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || !s.Binding.Valid() || s.Tenant != h.Tenant || !canonicalID(s.Task) || !canonicalID(s.Operation) || len(s.Prompt) == 0 || len(s.Prompt) > 64<<10 || len(s.Class) == 0 || len(s.Class) > 128 || len(s.Destination) == 0 || len(s.Destination) > 256 || s.MaxOutput < 1 || s.MaxOutput > 32768 {
		fail(400)
		return
	}
	// Hash the exact submitted bytes. The owner task commits this digest before
	// delivery; no language-dependent JSON canonicalization is assumed.
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(r.Context(), OperationTimeout)
	defer cancel()
	fact, e := h.Client.Check(ctx, token, s.Binding, s.Task, s.Operation, digest, "begin")
	if e != nil {
		fail(403)
		return
	}
	if h.Authenticated != nil && !h.Authenticated(w, r) {
		// Admission is consumed even if the shared connection pool is full.
		_, _ = h.Client.Check(ctx, token, s.Binding, s.Task, s.Operation, digest, "cancel")
		return
	}
	// No automatic retry follows begin. Even a lost response leaves the durable
	// operation consumed, so a restarted Conductor cannot duplicate dispatch.
	leaseCtx, outcome := operationoutcome.Track(ctx)
	// Each poll must finish before the most recently granted deadline. Failure
	// cancels provider work; the caller sees an uncertain result, not a retry.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		deadline := fact.Expires
		expiry := time.NewTimer(time.Until(time.Unix(0, int64(deadline*1e9))))
		defer expiry.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-expiry.C:
				cancel()
				return
			case <-ticker.C:
				checkCtx, stop := context.WithDeadline(ctx, time.Unix(0, int64(deadline*1e9)))
				next, err := h.Client.Check(checkCtx, token, s.Binding, s.Task, s.Operation, digest, "check")
				stop()
				if err != nil || next.Owner != fact.Owner || next.Principal != fact.Principal || next.Organization != fact.Organization || next.Device != fact.Device {
					cancel()
					return
				}
				deadline = next.Expires
				if !expiry.Stop() {
					select {
					case <-expiry.C:
					default:
					}
				}
				expiry.Reset(time.Until(time.Unix(0, int64(deadline*1e9))))
			}
		}
	}()
	// Renewals only extend the short current-authority check inside the fixed
	// five-minute operation bound. They never permit another dispatch.
	body, _ := json.Marshal(map[string]any{"model": s.Destination, "messages": []map[string]string{{"role": "user", "content": s.Prompt}}, "max_tokens": s.MaxOutput})
	local, _ := http.NewRequestWithContext(taskscope.With(leaseCtx, taskscope.Binding{Owner: taskscope.Owner{Tenant: fact.Tenant, Organization: fact.Organization, Owner: fact.Owner}, Task: fact.Task}), "POST", "http://localhost/v1/chat/completions", bytes.NewReader(body))
	local.Header.Set("Content-Type", "application/json")
	local.Header.Set("X-Conductor-Task", s.Task)
	local.Header.Set("X-Conductor-Step", s.Operation)
	local.Header.Set("X-Conductor-Class", s.Class)
	local.Header.Set("X-Conductor-Expected-Model", s.Destination)
	local.Header.Set(h.ProxyHeader, h.ProxyToken())
	if fact.MoneyCurrency != "" {
		local = local.WithContext(spendlimit.With(local.Context(), spendlimit.Admission{Currency: fact.MoneyCurrency, Reserve: func(ctx context.Context, amount int64, source string) error {
			return h.Client.reserveMoney(ctx, token, s, digest, fact.MoneyCurrency, amount, source)
		}}))
	}
	h.Dispatch.ServeHTTP(w, local)
	command := "complete"
	if leaseCtx.Err() != nil || !outcome.Known() {
		command = "cancel"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	_, _ = h.Client.Check(finishCtx, token, s.Binding, s.Task, s.Operation, digest, command)
}
