// Package privatefile atomically initializes service-owned identity files.
package privatefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// PublishOnce links a complete mode-0600 file into place without replacing a
// concurrent winner. Callers subsequently read and verify the published file.
func PublishOnce(dir, path string, data []byte) error {
	file, err := os.CreateTemp(dir, ".identity-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := os.Remove(file.Name()); err != nil {
		return err
	}
	return SyncDirectory(dir)
}

// Replace publishes the complete private file and syncs the directory entry.
func Replace(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".private-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return SyncDirectory(dir)
}

// SyncDirectory persists new directory entries on the school's Linux volume.
func SyncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
