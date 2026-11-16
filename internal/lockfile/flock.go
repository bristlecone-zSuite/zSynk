//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package lockfile

import (
	"errors"
	"os"
	"syscall"
)

// flock locks belong to the open file description, so a second open of the
// same file conflicts even within one process.
func openLocked(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
