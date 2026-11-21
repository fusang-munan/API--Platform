//go:build !windows

package beichen

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func restrictLocalSessionFile(file *os.File) error {
	return file.Chmod(0600)
}

func tryLocalSessionLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return err == nil, err
}
