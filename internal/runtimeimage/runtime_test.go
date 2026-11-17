package runtimeimage

import (
	"crypto/x509"
	"database/sql"
	"debug/elf"
	_ "modernc.org/sqlite"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These contracts run inside the final image only in its existing CI job.
func requireImage(t *testing.T) {
	t.Helper()
	if os.Getenv("CONDUCTOR_RUNTIME_CONTRACT") != "1" {
		t.Skip("final-image contract runs in CI")
	}
}
func TestRuntimeIdentityAndContents(t *testing.T) {
	requireImage(t)
	if os.Getuid() != 65532 || os.Getgid() != 65532 {
		t.Fatal("runtime must use the assigned non-root identity")
	}
	release, err := os.ReadFile("/etc/alpine-release")
	if err != nil || !strings.HasPrefix(string(release), "3.23.") {
		t.Fatal("unexpected runtime base")
	}
	for _, path := range []string{"/src", "/out", "/qualified", "/usr/local/go", "/app/.git", "/usr/local/bin/runtime-contract.test"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected build path or inaccessible check: %s (%v)", path, err)
		}
	}
	for _, tool := range []string{"go", "gcc", "g++", "make", "git"} {
		if _, err := exec.LookPath(tool); err == nil {
			t.Fatalf("build tool in runtime: %s", tool)
		}
	}
	binary, err := elf.Open("/usr/local/bin/conductor")
	if err != nil {
		t.Fatal(err)
	}
	defer binary.Close()
	libraries, err := binary.ImportedLibraries()
	if err != nil || len(libraries) != 0 {
		t.Fatalf("binary needs dynamic libraries: %v %v", libraries, err)
	}
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP {
			t.Fatal("binary requires a dynamic interpreter")
		}
	}
}
func TestRuntimeSystemRoots(t *testing.T) {
	requireImage(t)
	roots, err := x509.SystemCertPool()
	if err != nil || len(roots.Subjects()) == 0 {
		t.Fatal("system TLS roots unavailable", err)
	}
}
func TestRuntimeSQLiteState(t *testing.T) {
	requireImage(t)
	dir, err := os.MkdirTemp("/var/lib/conductor", "contract-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE checks (value TEXT); INSERT INTO checks VALUES ('persisted')"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err = db.QueryRow("SELECT value FROM checks").Scan(&value); err != nil || value != "persisted" {
		t.Fatal("state did not survive reopen", err)
	}
}
