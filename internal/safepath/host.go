package safepath

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Within reports whether the absolute path p is dir or inside it. Volumes on
// macOS and Windows are case-insensitive by default, so case is ignored
// there.
func Within(p, dir string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		p, dir = strings.ToLower(p), strings.ToLower(dir)
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && filepath.IsLocal(rel)
}

// Resolve returns p with symlinks resolved in its deepest existing folder,
// so a folder that doesn't exist yet is judged by where it would be
// created.
func Resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	var rest []string
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(append([]string{p}, rest...)...)
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}
