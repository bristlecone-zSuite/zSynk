//go:build unix

package blob

import (
	"os"
	"syscall"
)

// lockExclusive takes an advisory exclusive lock on an open file without
// waiting. It fails when another transfer holds the lock.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
