//go:build unix

package atomicfile

import (
	"os"
	"path/filepath"
	"syscall"
)

func diskAvailable(path string) (uint64, error) {
	dir := path
	st, err := os.Stat(path)
	if err != nil {
		dir = filepath.Dir(path)
	} else if !st.IsDir() {
		dir = filepath.Dir(path)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func sameVolume(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return true
	}
	return sa.Dev == sb.Dev
}
