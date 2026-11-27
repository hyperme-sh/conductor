package cloudworkspace

import (
	"encoding/json"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func openGrant(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, refused
	}
	file := os.NewFile(uintptr(fd), "workspace lease")
	var meta unix.Stat_t
	if unix.Fstat(fd, &meta) != nil || meta.Uid != 0 || meta.Mode&unix.S_IFMT != unix.S_IFREG ||
		meta.Mode&022 != 0 || meta.Nlink != 1 || meta.Size > 4096 {
		file.Close()
		return nil, refused
	}
	return file, nil
}

func checkJSON(grant Grant) error {
	root, err := unix.Open(grant.Workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return refused
	}
	defer unix.Close(root)
	fd, err := unix.Openat(root, grant.Target, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return refused
	}
	file := os.NewFile(uintptr(fd), "declared JSON")
	defer file.Close()
	meta, err := file.Stat()
	if err != nil || !meta.Mode().IsRegular() || meta.Size() > 65536 {
		return refused
	}
	content, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(content) > 65536 || !json.Valid(content) {
		return refused
	}
	return nil
}
