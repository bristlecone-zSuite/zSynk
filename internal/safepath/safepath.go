// Package safepath validates library-relative paths that come from peers,
// package manifests, or the filesystem before they are joined onto a
// directory on disk.
package safepath

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrUnsafe = errors.New("safepath: path is not a clean path inside the library")

// SynkDir is Synk's own directory in each library root. It marks the folder
// as a library and holds staging; it is never scanned or synced.
const SynkDir = ".synk"

// Check accepts slash-separated relative paths such as "notes/todo.md". It
// rejects anything that is empty, absolute, unclean, escapes with "..",
// contains a backslash or NUL, isn't valid UTF-8 (JSON would rewrite it in
// transit), or names Synk's own .synk directory at any depth.
func Check(rel string) error {
	if rel == "" || rel == "." || path.Clean(rel) != rel || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return ErrUnsafe
	}
	if strings.ContainsAny(rel, "\\\x00") || !utf8.ValidString(rel) {
		return ErrUnsafe
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || IsSynkDir(part) {
			return ErrUnsafe
		}
	}
	return nil
}

// IsSynkDir reports whether a single path component names the .synk
// directory. APFS is case-insensitive by default, so ".SYNK" counts.
func IsSynkDir(name string) bool {
	return strings.EqualFold(name, SynkDir)
}
