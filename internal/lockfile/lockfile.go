// Package lockfile gives one process at a time a folder to itself. The lock
// belongs to an open file, so the OS releases it when the process exits,
// even if it crashes.
package lockfile

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// ErrLocked means another open lock holds the file, in this process or
// another.
var ErrLocked = errors.New("lockfile: locked")

type Lock struct {
	f *os.File
}

// Acquire takes the lock at path without waiting, creating the file if
// needed, and records this process's ID in it.
func Acquire(path string) (*Lock, error) {
	f, err := openLocked(path)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// Holder returns the process ID recorded at path, or 0 if there isn't one.
func Holder(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// Release gives the lock up. Releasing it twice does nothing.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
