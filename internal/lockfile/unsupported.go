//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package lockfile

import (
	"errors"
	"os"
)

// Without a lock, two processes could write the same folder at once.
func openLocked(string) (*os.File, error) {
	return nil, errors.ErrUnsupported
}
