//go:build linux

package codingloop

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func openFile(project, path string, uid uint32, write bool) (*os.File, string, error) {
	if uid == 0 || !filepath.IsAbs(project) || filepath.Clean(project) != project || project == "/" || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return nil, "", Err
	}
	parts := strings.Split(strings.TrimPrefix(project, "/"), "/")
	parts = append(parts, strings.Split(path, "/")...)
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, "", Err
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." || part == ".git" {
			unix.Close(fd)
			return nil, "", Err
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, "", Err
		}
		fd = next
	}
	var parent unix.Stat_t
	if unix.Fstat(fd, &parent) != nil {
		unix.Close(fd)
		return nil, "", Err
	}
	flags := unix.O_RDONLY
	if write {
		flags = unix.O_RDWR
	}
	flags |= unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	filefd, e := unix.Openat(fd, parts[len(parts)-1], flags, 0)
	unix.Close(fd)
	if e != nil {
		return nil, "", Err
	}
	f := os.NewFile(uintptr(filefd), path)
	var st unix.Stat_t
	if unix.Fstat(filefd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uid || st.Nlink != 1 || st.Size > 10000 {
		f.Close()
		return nil, "", Err
	}
	if write && (st.Mode&0200 == 0 || unix.Flock(filefd, unix.LOCK_EX|unix.LOCK_NB) != nil) {
		f.Close()
		return nil, "", Err
	}
	return f, digest([]byte(fmt.Sprintf("%d:%d:%d", parent.Dev, parent.Ino, parent.Uid))), nil
}
func readFile(project, path string, uid uint32) ([]byte, error) {
	f, _, e := openFile(project, path, uid, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 10001))
	if e != nil || len(raw) > 10000 {
		return nil, Err
	}
	return raw, nil
}
func reviewedRead(project, path string, uid uint32, review func(int64, string) error, ctx context.Context) ([]byte, error) {
	f, _, e := openFile(project, path, uid, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, Err
	}
	mark := digest([]byte(fmt.Sprintf("%v", st.Sys())))
	if review(st.Size(), mark) != nil || ctx.Err() != nil {
		return nil, Err
	}
	after, e := f.Stat()
	if e != nil || digest([]byte(fmt.Sprintf("%v", after.Sys()))) != mark {
		return nil, Err
	}
	raw, e := io.ReadAll(io.LimitReader(f, st.Size()))
	if e != nil || int64(len(raw)) != st.Size() || ctx.Err() != nil {
		return nil, Err
	}
	final, e := f.Stat()
	if e != nil || final.Size() != st.Size() || final.ModTime() != st.ModTime() {
		return nil, Err
	}
	return raw, nil
}
func replaceFile(project, path string, uid uint32, before string, content []byte, review func(string) error, ctx context.Context) error {
	f, parent, e := openFile(project, path, uid, true)
	if e != nil {
		return e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 10001))
	if e != nil || digest(raw) != before {
		return Err
	}
	if e = review(parent); e != nil {
		return e
	}
	current, _, e := openFile(project, path, uid, false)
	if e != nil {
		return e
	}
	defer current.Close()
	a, e := f.Stat()
	if e != nil {
		return Err
	}
	b, e := current.Stat()
	if e != nil || !os.SameFile(a, b) {
		return Err
	}
	raw, e = io.ReadAll(io.LimitReader(current, 10001))
	if e != nil || digest(raw) != before || ctx.Err() != nil {
		return Err
	}
	if _, e = f.WriteAt(content, 0); e != nil {
		return Err
	}
	if f.Truncate(int64(len(content))) != nil || f.Sync() != nil {
		return Err
	}
	return nil
}

type bounded struct {
	mu        sync.Mutex
	b         bytes.Buffer
	truncated bool
}

func (b *bounded) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := 3000 - b.b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.b.Write(p)
	return n, nil
}
func trustedBinary(path, hash string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		st, e := os.Lstat(dir)
		if e != nil || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return Err
		}
		var raw unix.Stat_t
		if unix.Lstat(dir, &raw) != nil || raw.Uid != 0 {
			return Err
		}
		if dir == "/" {
			break
		}
	}
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return Err
	}
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(int(f.Fd()), &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0022 != 0 {
		return Err
	}
	data, e := io.ReadAll(io.LimitReader(f, 128<<20))
	if e != nil || len(data) == 128<<20 || hash != "" && digest(data) != hash {
		return Err
	}
	return nil
}
func runCheck(ctx context.Context, check Check, files []file, uid uint32) (string, bool, error) {
	if uid == 0 || (os.Geteuid() != 0 && uint32(os.Geteuid()) != uid) {
		return "", false, Err
	}
	if trustedBinary(check.Executable, check.SHA256) != nil || trustedBinary("/usr/bin/bwrap", "") != nil || trustedBinary("/usr/bin/prlimit", "") != nil {
		return "", false, Err
	}
	root, e := os.MkdirTemp("", "conductor-check-")
	if e != nil {
		return "", false, e
	}
	defer os.RemoveAll(root)
	if os.Geteuid() == 0 && os.Chown(root, int(uid), int(uid)) != nil {
		return "", false, Err
	}
	for _, f := range files {
		if !filepath.IsLocal(f.Path) {
			return "", false, Err
		}
		path := filepath.Join(root, f.Path)
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, []byte(f.Content), 0600) != nil {
			return "", false, Err
		}
	}
	if os.Geteuid() == 0 {
		if filepath.Walk(root, func(path string, _ os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			return os.Chown(path, int(uid), int(uid))
		}) != nil {
			return "", false, Err
		}
	}
	filter, e := processFilter()
	if e != nil {
		return "", false, e
	}
	defer filter.Close()
	args := []string{"--seccomp", "3", "--unshare-all", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv"}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, e := os.Stat(path); e == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--size", "16777216", "--tmpfs", "/tmp", "--ro-bind", root, "/workspace", "--chdir", "/workspace", "--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin", "--setenv", "HOME", "/tmp", "--", check.Executable)
	args = append(args, check.Args...)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	limits := []string{"--as=268435456:268435456", "--cpu=15:15", "--fsize=16777216:16777216", "--nofile=64:64", "--", "/usr/bin/bwrap"}
	cmd := exec.CommandContext(ctx, "/usr/bin/prlimit", append(limits, args...)...)
	cmd.ExtraFiles = []*os.File{filter}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{}}}
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.WaitDelay = time.Second
	output := &bounded{}
	cmd.Stdout = output
	cmd.Stderr = output
	e = cmd.Run()
	if ctx.Err() != nil {
		return "", false, Err
	}
	text := output.b.String()
	if output.truncated {
		text += "\n[output truncated]"
	}
	if e == nil {
		return text, true, nil
	}
	if _, ok := e.(*exec.ExitError); ok {
		return text, false, nil
	}
	return "", false, Err
}

// Checks are single-process: a process namespace alone cannot bound a fork bomb.
// The architecture guard also prevents switching syscall ABIs to evade the filter.
func processFilter() (*os.File, error) {
	var arch uint32
	var denied []uint32
	switch runtime.GOARCH {
	case "amd64":
		arch = 0xc000003e
		denied = []uint32{56, 57, 58, 435}
	case "arm64":
		arch = 0xc00000b7
		denied = []uint32{220, 435}
	default:
		return nil, Err
	}
	filters := []unix.SockFilter{{Code: 0x20, K: 4}, {Code: 0x15, Jt: 1, K: arch}, {Code: 0x06, K: 0x80000000}, {Code: 0x20, K: 0}}
	// Reject x32 and any unsupported high syscall number as well.
	filters = append(filters, unix.SockFilter{Code: 0x35, Jf: 1, K: 0x40000000}, unix.SockFilter{Code: 0x06, K: 0x00050001})
	for _, nr := range denied {
		filters = append(filters, unix.SockFilter{Code: 0x15, Jf: 1, K: nr}, unix.SockFilter{Code: 0x06, K: 0x00050001})
	}
	filters = append(filters, unix.SockFilter{Code: 0x06, K: 0x7fff0000})
	fd, e := unix.MemfdCreate("check-filter", unix.MFD_CLOEXEC)
	if e != nil {
		return nil, Err
	}
	f := os.NewFile(uintptr(fd), "check-filter")
	if binary.Write(f, binary.LittleEndian, filters) != nil {
		f.Close()
		return nil, Err
	}
	if _, e = f.Seek(0, 0); e != nil {
		f.Close()
		return nil, Err
	}
	return f, nil
}
