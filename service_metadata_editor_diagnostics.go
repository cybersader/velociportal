package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Read-only prerequisite inspection: no lock creation, sync, temporary file or
// probe write. Passing does not prove rename/fsync capability or lock availability.
func inspectServiceMetadataEditorStorage(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errMetadataStorage
	}
	dir := filepath.Dir(path)
	if validateEditorAncestors(dir) != nil {
		return errMetadataStorage
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errMetadataStorage
	}
	file := os.NewFile(uintptr(fd), dir)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !safeEditorDirectory(info) {
		return errMetadataStorage
	}
	e := &ServiceMetadataEditor{path: path, base: filepath.Base(path), directory: file}
	data, target, err := e.readFile()
	if err != nil {
		return errMetadataStorage
	}
	defer target.Close()
	if _, err := parseServiceMetadata(data); err != nil {
		return errMetadataStorage
	}
	return nil
}
