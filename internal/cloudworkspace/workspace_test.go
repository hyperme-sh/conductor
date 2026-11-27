package cloudworkspace

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkspaceScopeLeaseAndReconnect(t *testing.T) {
	now := time.Now()
	grant := Grant{
		Version: 1, Scope: Scope{uuid.New(), 1, uuid.New(), uuid.New(), "/private/project"},
		Expires: now.Add(time.Minute).UnixMilli(), Tool: "check-json", Target: "fixture.json",
	}
	source := grant
	var mu sync.Mutex
	clock := now
	available := true
	started, finish := make(chan struct{}), make(chan struct{})
	h := &Handler{
		read: func() (Grant, error) {
			mu.Lock()
			defer mu.Unlock()
			if !available {
				return Grant{}, refused
			}
			return source, nil
		},
		now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return clock
		},
		check: func(Grant) error {
			close(started)
			<-finish
			return nil
		},
	}
	request := Request{Scope: grant.Scope, Operation: "inspect"}
	inspected, err := h.exchange(request)
	if err != nil || inspected.State != "ready" || inspected.Grant != grant {
		t.Fatalf("inspect: %v", err)
	}
	wrong := request
	wrong.Device = uuid.New()
	if _, err := h.exchange(wrong); err == nil {
		t.Fatal("wrong worker accepted")
	}
	request.Operation = "invoke"
	if result, err := h.exchange(request); err != nil || result.State != "running" {
		t.Fatalf("invoke: %v", err)
	}
	<-started
	request.Operation = "read"
	page, err := h.exchange(request)
	if err != nil || page.Output != "Checking fixture.json\n" || page.State != "running" {
		t.Fatalf("running output: %v", err)
	}
	close(finish)
	request.Cursor = page.Cursor
	deadline := time.Now().Add(time.Second)
	for {
		page, err = h.exchange(request)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("reattach: %v", err)
		}
		if page.State == "completed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if page.Output != "Declared JSON file is valid.\n" {
		t.Fatal("reattach missed output")
	}
	request.Cursor = 0
	request.Operation = "invoke"
	if replay, err := h.exchange(request); err != nil || replay.State != "completed" {
		t.Fatal("invocation replay did not retain the original run")
	}
	mu.Lock()
	available = false
	mu.Unlock()
	if _, err := h.exchange(request); err == nil {
		t.Fatal("revoked lease accepted")
	}
	mu.Lock()
	available = true
	clock = now.Add(time.Minute)
	mu.Unlock()
	if _, err := h.exchange(request); err == nil {
		t.Fatal("expired lease accepted")
	}
	mu.Lock()
	source.Generation++
	source.Lease = uuid.New()
	source.Expires = clock.Add(time.Minute).UnixMilli()
	next := source
	mu.Unlock()
	if _, err := h.exchange(request); err == nil {
		t.Fatal("old worker generation accepted")
	}
	if replaced, err := h.exchange(Request{Scope: next.Scope, Operation: "read"}); err != nil ||
		replaced.State != "ready" || replaced.Output != "" {
		t.Fatal("replacement read output from the former worker")
	}
}
