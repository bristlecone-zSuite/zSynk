//go:build !unix

package atomicfile

import "fmt"

func diskAvailable(path string) (uint64, error) {
	return 0, fmt.Errorf("atomicfile: disk space check not implemented on this OS")
}

func sameVolume(a, b string) bool { return true }
