package fsmeta

import (
	"os"
	"path/filepath"
	"strings"
)

// FoldsCase reports whether the file system holding dir treats names that
// differ only in case as the same file. It probes by creating a file and
// looking for it under a case-swapped name, which is the only way to ask
// the file system what it actually does. A volume that can't be probed
// counts as case-sensitive, which is the safer answer: a case-sensitive
// volume can hold colliding names, so no path has to move.
func FoldsCase(dir string) bool {
	// Probe inside the library's own .synk folder when there is one, so a
	// scan running at the same time never sees the probe.
	probe := dir
	if st, err := os.Stat(filepath.Join(dir, ".synk")); err == nil && st.IsDir() {
		probe = filepath.Join(dir, ".synk")
	}
	f, err := os.CreateTemp(probe, ".synk-FoldProbe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(name)
	}()
	swapped := swapCase(name)
	if swapped == name {
		return false
	}
	// Lstat while the probe file still exists; removing it first would make
	// every volume look case-sensitive.
	_, err = os.Lstat(swapped)
	return err == nil
}

// swapCase flips every ASCII letter in a path, leaving the directory
// separators and everything else alone.
func swapCase(p string) string {
	dir, file := filepath.Split(p)
	var b strings.Builder
	b.Grow(len(file))
	for i := 0; i < len(file); i++ {
		c := file[i]
		switch {
		case 'a' <= c && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case 'A' <= c && c <= 'Z':
			b.WriteByte(c - 'A' + 'a')
		default:
			b.WriteByte(c)
		}
	}
	return dir + b.String()
}
