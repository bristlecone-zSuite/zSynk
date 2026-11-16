// Package logfile keeps a log in files of bounded size.
package logfile

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
)

// File appends to a log at a path. Once the log reaches its limit, it
// moves to the path plus ".1", replacing the one there, and a new log
// starts, so the two take at most twice the limit.
type File struct {
	mu    sync.Mutex
	path  string
	limit int64
	f     *os.File
	size  int64
}

// Open opens the log at path for appending, creating it if needed.
func Open(path string, limit int64) (*File, error) {
	l := &File{path: path, limit: limit}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *File) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *File) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size > 0 && l.size+int64(len(p)) > l.limit {
		_ = l.f.Close()
		_ = os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *File) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// Tail returns the last n lines of the log at path, oldest first. A log
// that doesn't exist has none.
func Tail(path string, n int) ([]string, error) {
	var lines []string
	for _, p := range []string{path + ".1", path} {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if s := strings.TrimSuffix(string(b), "\n"); s != "" {
			lines = append(lines, strings.Split(s, "\n")...)
		}
	}
	return lines[max(0, len(lines)-n):], nil
}
