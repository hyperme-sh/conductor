package machineingress

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hyperme-sh/hypermesh-conductor/internal/operationoutcome"
)

func submission() Submission {
	return Submission{Binding: Binding{Version: 1, Tenant: uuid.NewString(), Device: uuid.NewString(), Bot: uuid.NewString(), Assignment: uuid.NewString(), Session: uuid.NewString()}, Task: uuid.NewString(), Operation: uuid.NewString(), Class: "coding", Destination: "local-model", Prompt: "sample", MaxOutput: 128}
}
func authority(t *testing.T, s Submission, mutate func(*Fact)) (*httptest.Server, *Client) {
	t.Helper()
	var mu sync.Mutex
	begun := false
	owner, organization := uuid.NewString(), uuid.NewString()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hm_msi_test" || r.Header.Get("X-Tenant-ID") != s.Tenant {
			t.Error("credential binding missing")
			w.WriteHeader(401)
			return
		}
		kind := "machine"
		if s.Enrollment != "" {
			kind = "phone"
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/hypermesh/bots/"+s.Bot+"/"+kind+"/") {
			t.Error("wrong path")
			w.WriteHeader(404)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["device_id"] != s.Device || body["assignment_id"] != s.Assignment || body["session_id"] != s.Session {
			w.WriteHeader(403)
			return
		}
		f := Fact{Version: 1, Request: body["request_id"].(string), Tenant: s.Tenant, Owner: owner, Principal: "owner", Organization: organization, Device: s.Device, Bot: s.Bot, Assignment: s.Assignment, Session: s.Session, Expires: float64(time.Now().UnixNano())/1e9 + 10}
		if command, ok := body["command"].(string); ok {
			mu.Lock()
			defer mu.Unlock()
			if command == "begin" {
				if begun {
					w.WriteHeader(409)
					return
				}
				begun = true
			}
			f.Task = s.Task
			f.Operation = s.Operation
			f.Digest = body["request_sha256"].(string)
			f.State = "active"
			if command == "complete" {
				f.State = "completed"
			}
			if command == "cancel" {
				f.State = "unknown"
			}
		}
		if s.Enrollment != "" {
			if body["enrollment_id"] != s.Enrollment {
				t.Error("enrollment not bound")
			}
			f.CallerDevice = s.Device
			f.CallerEnrollment = s.Enrollment
			f.Device = organization // independent stable execution identity in fixture
		}
		if mutate != nil {
			mutate(&f)
		}
		json.NewEncoder(w).Encode(f)
	}))
	return server, &Client{Origin: server.URL, HTTP: server.Client(), Phone: s.Enrollment != ""}
}
func TestOwnerContextBindingAndCredentialIsolation(t *testing.T) {
	s := submission()
	server, c := authority(t, s, nil)
	defer server.Close()
	fact, err := c.Check(t.Context(), "hm_msi_test", s.Binding, "", "", "", "")
	if err != nil || fact.Device != s.Device {
		t.Fatal(fact, err)
	}
	for _, credential := range []string{"renter-key", "ey.jwt.token", "hm_dev_bootstrap", "hm_bot_secret"} {
		if _, err := c.Check(t.Context(), credential, s.Binding, "", "", "", ""); err == nil {
			t.Fatal("accepted", credential)
		}
	}
	for _, mutate := range []func(*Fact){func(f *Fact) { f.Tenant = uuid.NewString() }, func(f *Fact) { f.Session = uuid.NewString() }, func(f *Fact) { f.Expires = 0 }, func(f *Fact) { f.Expires += 100 }, func(f *Fact) { f.Device = uuid.NewString() }} {
		server, c := authority(t, s, mutate)
		if _, err := c.Check(t.Context(), "hm_msi_test", s.Binding, "", "", "", ""); err == nil {
			t.Error("accepted altered fact")
		}
		server.Close()
	}
}
func TestOneDispatchAndNoMachineCredentialToProvider(t *testing.T) {
	t.Run("host", func(t *testing.T) { checkOneDispatch(t, false) })
	t.Run("phone", func(t *testing.T) { checkOneDispatch(t, true) })
}
func checkOneDispatch(t *testing.T, phone bool) {
	s := submission()
	if phone {
		s.Enrollment = uuid.NewString()
	}
	server, c := authority(t, s, nil)
	defer server.Close()
	calls := 0
	h, err := New(*c, s.Tenant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		operationoutcome.Record(r.Context(), true)
		if r.Header.Get("X-Conductor-Expected-Model") != s.Destination {
			t.Error("destination not enforced")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Local-Token") != "local-only" {
			t.Error("credential leaked")
		}
		if r.Header.Get("X-Conductor-Task") != s.Task || r.Header.Get("X-Conductor-Step") != s.Operation {
			t.Error("operation not bound")
		}
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"model":"local-model"`)) {
			t.Error("destination missing")
		}
		w.Write([]byte(`{"ok":true}`))
	}), "X-Local-Token", func() string { return "local-only" })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/v1/machine/prompt-submit", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer hm_msi_test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i == 0 && w.Code != 200 || i == 1 && w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestRevocationCancelsProviderWithoutRetry(t *testing.T) {
	s := submission()
	count := 0
	server, c := authority(t, s, func(f *Fact) {
		count++
		if count > 1 {
			f.Expires = 0
		}
	})
	defer server.Close()
	calls := 0
	h, _ := New(*c, s.Tenant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		select {
		case <-r.Context().Done():
		case <-time.After(4 * time.Second):
			t.Error("revocation did not cancel")
		}
		w.WriteHeader(503)
	}), "X-Local", func() string { return "local" })
	raw, _ := json.Marshal(s)
	r := httptest.NewRequest("POST", "/v1/machine/prompt-submit", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer hm_msi_test")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestOriginAndTenantRefusals(t *testing.T) {
	for _, origin := range []string{"http://localhost", "https://user:secret@example.test", "https://example.test/?token=x", "https://example.test/other"} {
		if ValidOrigin(origin) {
			t.Fatal(origin)
		}
	}
	s := submission()
	server, c := authority(t, s, nil)
	defer server.Close()
	h, _ := New(*c, uuid.NewString(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("wrong tenant dispatched") }), "X-Local", func() string { return "local" })
	raw, _ := json.Marshal(s)
	r := httptest.NewRequest("POST", "/v1/machine/prompt-submit", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer hm_msi_test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestUnknownProviderOutcomeRemainsUnknown(t *testing.T) {
	s := submission()
	var final string
	server, c := authority(t, s, func(f *Fact) {
		if f.State != "active" {
			final = f.State
		}
	})
	defer server.Close()
	h, _ := New(*c, s.Tenant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operationoutcome.Record(r.Context(), false)
		w.WriteHeader(502)
	}), "X-Local", func() string { return "local" })
	raw, _ := json.Marshal(s)
	r := httptest.NewRequest("POST", "/v1/machine/prompt-submit", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer hm_msi_test")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if final != "unknown" {
		t.Fatal(final)
	}
}

func TestPhoneCallerCannotBecomeExecutionHost(t *testing.T) {
	s := submission()
	s.Enrollment = uuid.NewString()
	server, c := authority(t, s, nil)
	defer server.Close()
	fact, err := c.Check(t.Context(), "hm_msi_test", s.Binding, s.Task, s.Operation, strings.Repeat("a", 64), "begin")
	if err != nil || fact.CallerDevice != s.Device || fact.Device == s.Device {
		t.Fatal(fact, err)
	}
	if _, err := c.Check(t.Context(), "hm_msi_test", s.Binding, "", "", "", ""); err == nil {
		t.Fatal("phone context accepted without operation")
	}
	for _, mutate := range []func(*Fact){
		func(f *Fact) { f.CallerDevice = uuid.NewString() },
		func(f *Fact) { f.CallerEnrollment = uuid.NewString() },
		func(f *Fact) { f.Device = "" },
	} {
		server, c := authority(t, s, mutate)
		if _, err := c.Check(t.Context(), "hm_msi_test", s.Binding, s.Task, s.Operation, strings.Repeat("a", 64), "begin"); err == nil {
			t.Error("wrong phone binding accepted")
		}
		server.Close()
	}
}

func TestPhoneConnectionAdmissionAndRefusal(t *testing.T) {
	s := submission()
	s.Enrollment = uuid.NewString()
	server, c := authority(t, s, nil)
	defer server.Close()
	calls, admissions := 0, 0
	h, _ := New(*c, s.Tenant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }), "X-Local", func() string { return "local" })
	h.Authenticated = func(w http.ResponseWriter, r *http.Request) bool {
		admissions++
		w.Header().Set("Connection", "close")
		w.WriteHeader(503)
		return false
	}
	raw, _ := json.Marshal(s)
	for _, token := range []string{"invalid", "hm_msi_test", "hm_msi_test"} {
		r := httptest.NewRequest("POST", "/v1/phone/prompt-submit", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Connection") != "close" {
			t.Fatal("rejected connection retained")
		}
	}
	if calls != 0 || admissions != 1 {
		t.Fatalf("dispatch=%d admission=%d", calls, admissions)
	}
}
