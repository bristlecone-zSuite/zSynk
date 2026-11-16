package reconcile

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

type Kind string

const (
	KindFile    Kind = "file"
	KindPackage Kind = "package"
	KindDir     Kind = "dir"
)

// Entry is one path in a folder index. Deleted entries are tombstones.
type Entry struct {
	Path    string        `json:"path"`
	Kind    Kind          `json:"kind"`
	Hash    string        `json:"hash,omitempty"`
	Size    int64         `json:"size,omitempty"`
	Deleted bool          `json:"deleted,omitempty"`
	Vector  vector.Vector `json:"vector"`
	// Incomplete means this device could not confirm the path (iCloud
	// placeholder, permission error). It must never be treated as a delete.
	Incomplete bool `json:"incomplete,omitempty"`
	// Parts are interior blob hashes for packages. Regular files leave this empty.
	Parts []string `json:"parts,omitempty"`
	Meta
}

// Meta is what a version carries besides its content.
type Meta struct {
	// Exec is whether a file is executable. Changing it makes a new
	// version.
	Exec bool `json:"exec,omitempty"`
	// MTime is the modification time, in Unix nanoseconds, on the device
	// that made the version; zero if unknown. Changing only the time
	// doesn't make a new version, so a touch doesn't sync.
	MTime int64 `json:"mtime,omitempty"`
	// By is the device that made the version.
	By string `json:"by,omitempty"`
}

type Index struct {
	FolderID string           `json:"folderId"`
	DeviceID string           `json:"deviceId"`
	Entries  map[string]Entry `json:"entries"`
}

type OpKind int

const (
	OpSet OpKind = iota
	OpDelete
	OpConflict
	OpMergeVector
)

type Op struct {
	Kind          OpKind
	Path          string
	KindFile      Kind
	Hash          string
	Size          int64
	Deleted       bool
	Vector        vector.Vector
	ConflictPath  string
	ConflictKind  Kind
	ConflictHash  string
	ConflictSize  int64
	Parts         []string
	ConflictParts []string
	Meta          Meta
	ConflictMeta  Meta
}

func (k OpKind) String() string {
	switch k {
	case OpSet:
		return "set"
	case OpDelete:
		return "delete"
	case OpConflict:
		return "conflict"
	case OpMergeVector:
		return "merge-vector"
	default:
		return "unknown"
	}
}

func NewIndex(folderID, deviceID string) *Index {
	return &Index{FolderID: folderID, DeviceID: deviceID, Entries: map[string]Entry{}}
}

func (idx *Index) List() []Entry {
	if idx == nil {
		return nil
	}
	out := make([]Entry, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (idx *Index) Get(path string) (Entry, bool) {
	e, ok := idx.Entries[path]
	return e, ok
}

// Reconcile computes operations this device must apply to converge with remote.
// It never invents deletes from missing remote entries: tombstones must be explicit.
// Concurrent edits produce a winner at the original path, the same on every
// peer, plus a conflict copy named from the losing hash so both peers create
// the same files.
func Reconcile(local, remote *Index) []Op {
	if local == nil {
		local = NewIndex("", "")
	}
	if remote == nil {
		remote = NewIndex("", "")
	}
	paths := map[string]struct{}{}
	for p := range local.Entries {
		paths[p] = struct{}{}
	}
	for p := range remote.Entries {
		paths[p] = struct{}{}
	}
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)

	var ops []Op
	for _, p := range keys {
		l, hasL := local.Entries[p]
		r, hasR := remote.Entries[p]
		if !hasR {
			continue // remote didn't mention it; we may push it, we do not delete it
		}
		if r.Incomplete {
			continue
		}
		if hasL && l.Incomplete {
			continue
		}
		if op, ok := reconcilePair(l, hasL, r); ok {
			ops = append(ops, op)
		}
	}
	return ops
}

func reconcilePair(l Entry, hasL bool, r Entry) (Op, bool) {
	if !hasL {
		if r.Deleted {
			return Op{}, false
		}
		return set(r, vector.Clone(r.Vector)), true
	}

	if l.Deleted && r.Deleted {
		merged := vector.Merge(l.Vector, r.Vector)
		if !vector.Equal(merged, l.Vector) {
			return Op{Kind: OpMergeVector, Path: l.Path, KindFile: l.Kind, Deleted: true, Vector: merged}, true
		}
		return Op{}, false
	}

	if r.Deleted && !l.Deleted {
		if vector.Dominates(r.Vector, l.Vector) {
			return Op{Kind: OpDelete, Path: l.Path, KindFile: l.Kind, Hash: l.Hash, Size: l.Size, Vector: vector.Clone(r.Vector), Meta: r.Meta}, true
		}
		if vector.Concurrent(r.Vector, l.Vector) {
			// Concurrent delete vs edit: keep the bytes. Never lose a write.
			merged := vector.Merge(l.Vector, r.Vector)
			return Op{Kind: OpMergeVector, Path: l.Path, KindFile: l.Kind, Hash: l.Hash, Size: l.Size, Vector: merged}, true
		}
		return Op{}, false
	}

	if l.Deleted && !r.Deleted {
		if vector.Dominates(l.Vector, r.Vector) {
			return Op{}, false
		}
		return set(r, vector.Merge(l.Vector, r.Vector)), true
	}

	// Both have live content.
	merged := vector.Merge(l.Vector, r.Vector)
	sameContent := l.Hash == r.Hash && l.Kind == r.Kind
	if sameContent && l.Exec == r.Exec {
		if !vector.Equal(merged, l.Vector) {
			return Op{Kind: OpMergeVector, Path: l.Path, KindFile: l.Kind, Hash: l.Hash, Size: l.Size, Vector: merged}, true
		}
		return Op{}, false
	}

	if vector.Dominates(r.Vector, l.Vector) {
		return set(r, vector.Clone(r.Vector)), true
	}
	if vector.Dominates(l.Vector, r.Vector) {
		return Op{}, false
	}

	// Concurrent versions. Every peer picks the same winner.
	localWins := wins(l, r)
	win, lose := r, l
	if localWins {
		win, lose = l, r
	}
	if sameContent {
		// Only the executable bit differs, so there's nothing to keep a
		// copy of.
		if !localWins {
			return set(r, merged), true
		}
		if vector.Equal(merged, l.Vector) {
			return Op{}, false
		}
		return Op{Kind: OpMergeVector, Path: l.Path, KindFile: l.Kind, Hash: l.Hash, Size: l.Size, Vector: merged}, true
	}
	return Op{
		Kind:          OpConflict,
		Path:          l.Path,
		KindFile:      win.Kind,
		Hash:          win.Hash,
		Size:          win.Size,
		Vector:        merged,
		ConflictPath:  ConflictPath(l.Path, lose.Hash),
		ConflictKind:  lose.Kind,
		ConflictHash:  lose.Hash,
		ConflictSize:  lose.Size,
		Parts:         win.Parts,
		ConflictParts: lose.Parts,
		Meta:          win.Meta,
		ConflictMeta:  lose.Meta,
	}, true
}

// set is the op that gives this device r's version, with vector v.
func set(r Entry, v vector.Vector) Op {
	return Op{Kind: OpSet, Path: r.Path, KindFile: r.Kind, Hash: r.Hash, Size: r.Size, Vector: v, Parts: r.Parts, Meta: r.Meta}
}

// wins reports whether version a wins over a concurrent version b. The one
// modified later wins, as the edit an app most likely shows; the time only
// picks the winner, it never decides which version is newer. Ties go to
// the device ID that sorts last, then to the smaller hash, so the order is
// total and every peer picks the same winner.
func wins(a, b Entry) bool {
	switch {
	case a.MTime != b.MTime:
		return a.MTime > b.MTime
	case a.By != b.By:
		return a.By > b.By
	case a.Hash != b.Hash:
		return a.Hash < b.Hash
	case a.Kind != b.Kind:
		return a.Kind < b.Kind
	}
	return a.Exec && !b.Exec
}

// ConflictPath is deterministic across peers: original path + losing content hash.
func ConflictPath(orig, loserHash string) string {
	ext := filepath.Ext(orig)
	base := strings.TrimSuffix(orig, ext)
	short := loserHash
	if len(short) > 8 {
		short = short[:8]
	}
	return base + ".synk-conflict-" + short + ext
}

func IsConflictName(rel string) bool {
	return strings.Contains(filepath.Base(rel), ".synk-conflict-")
}
