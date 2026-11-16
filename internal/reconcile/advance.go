package reconcile

import (
	"path"
	"sort"

	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

// ScanItem is a live filesystem observation. Incomplete items are placeholders
// or unreadable paths and must not generate tombstones.
type ScanItem struct {
	Path       string
	Kind       Kind
	Hash       string
	Size       int64
	Incomplete bool
	Parts      []string
	Exec       bool
	// MTime is the modification time in Unix nanoseconds.
	MTime int64
}

// AdvanceOptions describes what a scan couldn't see and what it must not
// delete.
type AdvanceOptions struct {
	// Unconfirmed are paths the scan couldn't look at or into, such as
	// unreadable folders and names it skipped. Known entries at or under
	// them are marked Incomplete instead of deleted.
	Unconfirmed []string
	// Ignored reports paths the library ignores. Their entries are kept but
	// marked Incomplete: starting to ignore a path neither deletes it on
	// peers nor forgets its history here, which a later edit builds on.
	Ignored func(path string, isDir bool) bool
	// Hold lists paths whose deletes wait for confirmation. Their entries
	// stay as they were.
	Hold map[string]bool
	// NoExec means the library's file system doesn't keep executable bits,
	// so the scan's are ignored: known paths keep theirs, and new files
	// aren't executable.
	NoExec bool
}

// Advance builds the next index from a previous index and a fresh scan.
// Tombstones are created only when a previously known live path is
// confirmed gone; their paths are returned.
func Advance(prev *Index, deviceID string, scan []ScanItem, opts AdvanceOptions) (*Index, []string) {
	next := NewIndex("", deviceID)
	if prev != nil {
		next.FolderID = prev.FolderID
	}
	ignored := func(p string, k Kind) bool {
		return opts.Ignored != nil && opts.Ignored(p, k == KindPackage)
	}
	unconfirmed := map[string]bool{}
	for _, p := range opts.Unconfirmed {
		unconfirmed[p] = true
	}
	seen := map[string]ScanItem{}
	for _, s := range scan {
		if opts.NoExec {
			s.Exec = false
		}
		if !ignored(s.Path, s.Kind) {
			seen[s.Path] = s
		}
	}
	var deleted []string
	if prev != nil {
		for p, old := range prev.Entries {
			s, ok := seen[p]
			if ok {
				if opts.NoExec {
					s.Exec = old.Exec && s.Kind == KindFile
				}
				next.Entries[p] = evolve(old, s, deviceID)
				delete(seen, p)
				continue
			}
			switch {
			case old.Deleted, opts.Hold[p]:
				next.Entries[p] = old
			case old.Incomplete && len(old.Vector) == 0:
				// Seen only as a placeholder or unreadable; this device never
				// had a version of it to delete or to remember.
			case ignored(p, old.Kind), underAny(unconfirmed, p):
				old.Incomplete = true
				next.Entries[p] = old
			default:
				// Gone from a folder the scan read, with no placeholder left:
				// confirmed deleted, even if it was Incomplete last time.
				tomb := old
				tomb.Deleted = true
				tomb.Incomplete = false
				tomb.Vector = vector.Clone(old.Vector).Bump(deviceID)
				tomb.By = deviceID
				next.Entries[p] = tomb
				deleted = append(deleted, p)
			}
		}
	}
	for p, s := range seen {
		next.Entries[p] = newFromScan(s, deviceID)
	}
	sort.Strings(deleted)
	return next, deleted
}

// underAny reports whether p or one of its parent folders is in set. "."
// covers everything.
func underAny(set map[string]bool, p string) bool {
	if len(set) == 0 {
		return false
	}
	for ; p != "."; p = path.Dir(p) {
		if set[p] {
			return true
		}
	}
	return set["."]
}

func evolve(old Entry, s ScanItem, deviceID string) Entry {
	if s.Incomplete {
		old.Incomplete = true
		return old
	}
	if old.Deleted {
		// Bumped past the tombstone, so the new file wins over the delete.
		e := newFromScan(s, deviceID)
		e.Vector = vector.Clone(old.Vector).Bump(deviceID)
		return e
	}
	if s.Hash == old.Hash && s.Kind == old.Kind && s.Exec == old.Exec {
		old.Incomplete = false
		old.Size = s.Size
		old.Parts = s.Parts
		return old
	}
	old.Kind = s.Kind
	old.Hash = s.Hash
	old.Size = s.Size
	old.Deleted = false
	old.Incomplete = false
	old.Parts = s.Parts
	old.Meta = Meta{Exec: s.Exec, MTime: s.MTime, By: deviceID}
	old.Vector = vector.Clone(old.Vector).Bump(deviceID)
	return old
}

func newFromScan(s ScanItem, deviceID string) Entry {
	e := Entry{
		Path:       s.Path,
		Kind:       s.Kind,
		Hash:       s.Hash,
		Size:       s.Size,
		Incomplete: s.Incomplete,
		Parts:      s.Parts,
		Vector:     vector.New(),
	}
	if !s.Incomplete {
		e.Vector = e.Vector.Bump(deviceID)
		e.Meta = Meta{Exec: s.Exec, MTime: s.MTime, By: deviceID}
	}
	return e
}

func SortedPaths(idx *Index) []string {
	var keys []string
	for k := range idx.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
