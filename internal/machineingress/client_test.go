//go:build linux || darwin

package machineingress

import (
	"context"
	"github.com/hyperme-sh/hypermesh-conductor/internal/localtransport"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmitUsesVerifiedSocketAndExactOwnerBytes(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	socket := filepath.Join(dir, "conductor.sock")
	listener, e := localtransport.Listen(socket)
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{ "prompt": "` + strings.Repeat(`\n`, 50000) + `" }`)
	count := 0
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(raw) || r.Header.Get("Authorization") != "Bearer hm_msi_private" || r.URL.Path != "/v1/machine/prompt-submit" {
			t.Error("binding changed")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}]}`))
	})}
	go server.Serve(listener)
	defer server.Close()
	result, e := Submit(context.Background(), socket, "hm_msi_private", raw)
	if e != nil || len(result) == 0 {
		t.Fatal(e)
	}
	if count != 1 {
		t.Fatal(count)
	}
	os.Chmod(socket, 0666)
	if _, e = Submit(context.Background(), socket, "hm_msi_private", raw); e == nil {
		t.Fatal("untrusted peer accepted")
	}
}
func TestPlanResponseCanExpandBeyondPromptResponseLimit(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	socket := filepath.Join(dir, "c.sock")
	listener, e := localtransport.Listen(socket)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"preview":"`+strings.Repeat("x", 600000)+`"}`)
	})}
	go server.Serve(listener)
	defer server.Close()
	if _, e = privateCall(t.Context(), socket, "hm_msi_private", []byte(`{}`), "/v1/machine/plan"); e != nil {
		t.Fatal(e)
	}
	if _, e = Submit(t.Context(), socket, "hm_msi_private", []byte(`{}`)); e == nil {
		t.Fatal("prompt response bound widened")
	}
}
