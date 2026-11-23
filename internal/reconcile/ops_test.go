package reconcile

import (
	"reflect"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

// vec builds a vector with n ticks for device.
func vec(device string, n int) vector.Vector {
	v := vector.New()
	for i := 0; i < n; i++ {
		v = v.Bump(device)
	}
	return v
}

func TestOpKindString(t *testing.T) {
	for k, want := range map[OpKind]string{
		OpSet:         "set",
		OpDelete:      "delete",
		OpConflict:    "conflict",
		OpMergeVector: "merge-vector",
		OpKind(99):    "unknown",
	} {
		if got := k.String(); got != want {
			t.Errorf("OpKind(%d).String() = %q; want %q", k, got, want)
		}
	}
}

func TestList(t *testing.T) {
	if got := (*Index)(nil).List(); got != nil {
		t.Errorf("nil index List() = %#v; want nil", got)
	}
	idx := NewIndex("f", "alice")
	idx.Entries["b.md"] = Entry{Path: "b.md", Kind: KindFile}
	idx.Entries["a.md"] = Entry{Path: "a.md", Kind: KindFile}
	idx.Entries["c/b.md"] = Entry{Path: "c/b.md", Kind: KindFile}
	got := idx.List()
	want := []string{"a.md", "b.md", "c/b.md"}
	if len(got) != len(want) {
		t.Fatalf("List() returned %d entries; want %d", len(got), len(want))
	}
	for i, e := range got {
		if e.Path != want[i] {
			t.Errorf("List()[%d] = %q; want %q", i, e.Path, want[i])
		}
	}
}

func TestReconcile(t *testing.T) {
	if got := Reconcile(nil, nil); got != nil {
		t.Errorf("Reconcile(nil, nil) = %#v; want nil", got)
	}

	// Remote-only live entry becomes a set; remote-only tombstone does not.
	remote := NewIndex("f", "bob")
	remote.Entries["new.md"] = Entry{Path: "new.md", Kind: KindFile, Hash: "h2", Size: 2, Vector: vec("bob", 1), Meta: Meta{By: "bob"}}
	remote.Entries["gone.md"] = Entry{Path: "gone.md", Kind: KindFile, Deleted: true, Vector: vec("bob", 1)}
	ops := Reconcile(NewIndex("f", "alice"), remote)
	if len(ops) != 1 {
		t.Fatalf("Reconcile gave %d ops; want 1: %#v", len(ops), ops)
	}
	want := Op{Kind: OpSet, Path: "new.md", KindFile: KindFile, Hash: "h2", Size: 2, Vector: vec("bob", 1), Meta: Meta{By: "bob"}}
	if !reflect.DeepEqual(ops[0], want) {
		t.Errorf("op = %+v; want %+v", ops[0], want)
	}

	// Local-only entries produce nothing: no invented deletes or pushes here.
	local := NewIndex("f", "alice")
	local.Entries["mine.md"] = Entry{Path: "mine.md", Kind: KindFile, Hash: "h1", Vector: vec("alice", 1)}
	if ops := Reconcile(local, NewIndex("f", "alice")); len(ops) != 0 {
		t.Errorf("local-only Reconcile gave %d ops; want 0: %#v", len(ops), ops)
	}

	// Incomplete entries on either side are skipped.
	local.Entries["skip.md"] = Entry{Path: "skip.md", Kind: KindFile, Incomplete: true}
	remote.Entries["skip.md"] = Entry{Path: "skip.md", Kind: KindFile, Hash: "h9", Vector: vec("bob", 1)}
	remote.Entries["skip2.md"] = Entry{Path: "skip2.md", Kind: KindFile, Incomplete: true, Hash: "h8", Vector: vec("bob", 1)}
	if ops := Reconcile(local, remote); len(ops) != 1 {
		t.Errorf("Reconcile with incomplete entries gave %d ops; want 1: %#v", len(ops), ops)
	}
}

func TestReconcilePair(t *testing.T) {
	live := func(path, hash string, v vector.Vector) Entry {
		return Entry{Path: path, Kind: KindFile, Hash: hash, Size: 3, Vector: v, Meta: Meta{Exec: true, MTime: 10, By: "alice"}}
	}
	tomb := func(path string, v vector.Vector) Entry {
		return Entry{Path: path, Kind: KindFile, Deleted: true, Vector: v}
	}

	t.Run("missing local", func(t *testing.T) {
		if _, ok := reconcilePair(Entry{}, false, tomb("a.md", vec("bob", 1))); ok {
			t.Error("a remote tombstone with no local entry must be ignored")
		}
		r := live("a.md", "h1", vec("bob", 1))
		op, ok := reconcilePair(Entry{}, false, r)
		if !ok || op.Kind != OpSet || op.Path != "a.md" || op.Hash != "h1" || op.Size != 3 || !reflect.DeepEqual(op.Vector, vec("bob", 1)) || op.Meta.By != "alice" {
			t.Errorf("reconcilePair = %+v, %v; want a set of bob's version", op, ok)
		}
	})

	t.Run("both deleted", func(t *testing.T) {
		l := tomb("a.md", vec("alice", 1))
		if _, ok := reconcilePair(l, true, tomb("a.md", vec("alice", 1))); ok {
			t.Error("equal tombstones need no op")
		}
		op, ok := reconcilePair(l, true, tomb("a.md", vec("bob", 1)))
		if !ok || op.Kind != OpMergeVector || op.Path != "a.md" || !op.Deleted || !reflect.DeepEqual(op.Vector, vector.Merge(vec("alice", 1), vec("bob", 1))) {
			t.Errorf("reconcilePair = %+v, %v; want a merged tombstone vector", op, ok)
		}
	})

	t.Run("remote deleted", func(t *testing.T) {
		l := live("a.md", "h1", vec("alice", 1))
		if _, ok := reconcilePair(l, true, tomb("a.md", vec("alice", 1))); ok {
			t.Error("a delete we already dominate needs no op")
		}
		// Disjoint device sets are concurrent, so the bytes are kept and only
		// the vectors merge.
		op, ok := reconcilePair(l, true, tomb("a.md", vec("bob", 2)))
		if !ok || op.Kind != OpMergeVector || op.Path != "a.md" || op.Hash != "h1" || op.Size != 3 || !reflect.DeepEqual(op.Vector, vector.Merge(vec("alice", 1), vec("bob", 2))) {
			t.Errorf("concurrent delete vs edit = %+v, %v; want a vector merge keeping the bytes", op, ok)
		}
		// A delete on the same device that we haven't caught up with wins.
		op, ok = reconcilePair(live("a.md", "h1", vec("alice", 1)), true, tomb("a.md", vec("alice", 2)))
		if !ok || op.Kind != OpDelete || op.Path != "a.md" || op.Hash != "h1" || op.Size != 3 || !reflect.DeepEqual(op.Vector, vec("alice", 2)) {
			t.Errorf("reconcilePair = %+v, %v; want a delete", op, ok)
		}
	})

	t.Run("local deleted", func(t *testing.T) {
		l := tomb("a.md", vec("alice", 2))
		if _, ok := reconcilePair(l, true, live("a.md", "h1", vec("alice", 1))); ok {
			t.Error("our dominating tombstone needs no op")
		}
		r := live("a.md", "h1", vec("bob", 1))
		op, ok := reconcilePair(tomb("a.md", vec("alice", 1)), true, r)
		if !ok || op.Kind != OpSet || op.Hash != "h1" || !reflect.DeepEqual(op.Vector, vector.Merge(vec("alice", 1), vec("bob", 1))) {
			t.Errorf("reconcilePair = %+v, %v; want a set with the merged vector", op, ok)
		}
	})

	t.Run("both live", func(t *testing.T) {
		same := live("a.md", "h1", vec("alice", 1))
		if _, ok := reconcilePair(same, true, live("a.md", "h1", vec("alice", 1))); ok {
			t.Error("identical entries need no op")
		}
		op, ok := reconcilePair(same, true, live("a.md", "h1", vec("bob", 1)))
		if !ok || op.Kind != OpMergeVector || op.Hash != "h1" || !reflect.DeepEqual(op.Vector, vector.Merge(vec("alice", 1), vec("bob", 1))) {
			t.Errorf("same content, disjoint vectors = %+v, %v; want a vector merge", op, ok)
		}
		if _, ok := reconcilePair(live("a.md", "h1", vec("alice", 2)), true, live("a.md", "h2", vec("alice", 1))); ok {
			t.Error("a version we dominate needs no op")
		}
		op, ok = reconcilePair(live("a.md", "h1", vec("alice", 1)), true, live("a.md", "h2", vec("alice", 2)))
		if !ok || op.Kind != OpSet || op.Hash != "h2" || !reflect.DeepEqual(op.Vector, vec("alice", 2)) {
			t.Errorf("remote dominates = %+v, %v; want a set of the newer version", op, ok)
		}
	})

	t.Run("concurrent content", func(t *testing.T) {
		l := live("a.md", "h1", vec("alice", 1))
		r := live("a.md", "h2", vec("bob", 1))
		merged := vector.Merge(vec("alice", 1), vec("bob", 1))
		// Equal times and devices: the smaller hash wins, so alice's h1 stays
		// at a.md and bob's h2 becomes the conflict copy.
		op, ok := reconcilePair(l, true, r)
		if !ok || op.Kind != OpConflict || op.Path != "a.md" || op.Hash != "h1" || op.Size != 3 ||
			op.ConflictPath != ConflictPath("a.md", "h2") || op.ConflictHash != "h2" || op.ConflictSize != 3 ||
			!reflect.DeepEqual(op.Vector, merged) || op.Meta.By != "alice" || op.ConflictMeta.By != "alice" {
			t.Errorf("reconcilePair = %+v, %v; want h1 at a.md and an h2 conflict copy", op, ok)
		}
		// The winner flips with the later mtime, and the conflict copy with it.
		r.Meta.MTime = 20
		op, _ = reconcilePair(l, true, r)
		if op.Kind != OpConflict || op.Hash != "h2" || op.ConflictHash != "h1" || op.ConflictPath != ConflictPath("a.md", "h1") {
			t.Errorf("later mtime must win: op = %+v", op)
		}
	})

	t.Run("concurrent exec bit", func(t *testing.T) {
		l := live("a.md", "h1", vec("alice", 1))
		r := live("a.md", "h1", vec("bob", 1))
		r.Meta.Exec = false
		merged := vector.Merge(vec("alice", 1), vec("bob", 1))
		// The executable side wins the tie, so the local exec version keeps
		// the path and only the vector merges.
		op, ok := reconcilePair(l, true, r)
		if !ok || op.Kind != OpMergeVector || op.Hash != "h1" || !reflect.DeepEqual(op.Vector, merged) {
			t.Errorf("winning the exec tie = %+v, %v; want a vector merge", op, ok)
		}
		// With the exec bit on the remote side, the remote version wins the
		// path even though the content is the same.
		op, ok = reconcilePair(r, true, l)
		if !ok || op.Kind != OpSet || op.Hash != "h1" || !reflect.DeepEqual(op.Vector, merged) {
			t.Errorf("losing the exec tie = %+v, %v; want a set with the merged vector", op, ok)
		}
		// An already-merged vector needs no op at all.
		both := live("a.md", "h1", vector.Merge(vec("alice", 1), vec("bob", 1)))
		plain := live("a.md", "h1", vector.Merge(vec("alice", 1), vec("bob", 1)))
		plain.Meta.Exec = false
		if _, ok := reconcilePair(both, true, plain); ok {
			t.Error("winning the exec tie with an already-merged vector needs no op")
		}
	})
}

func TestWins(t *testing.T) {
	base := Entry{Hash: "h1", Kind: KindFile, Meta: Meta{MTime: 10, By: "alice"}}
	later := base
	later.Meta.MTime = 20
	if !wins(later, base) || wins(base, later) {
		t.Error("the later mtime must win")
	}
	byBob := base
	byBob.Meta.By = "bob"
	if !wins(byBob, base) || wins(base, byBob) {
		t.Error("the device ID that sorts last must win on an mtime tie")
	}
	lowHash := base
	lowHash.Hash = "a1"
	if !wins(lowHash, base) || wins(base, lowHash) {
		t.Error("the smaller hash must win on a device tie")
	}
	pkg := base
	pkg.Kind = KindPackage
	if !wins(base, pkg) || wins(pkg, base) {
		t.Error("the kind that sorts first must win on a full tie")
	}
	exec := base
	exec.Meta.Exec = true
	if !wins(exec, base) || wins(base, exec) {
		t.Error("the executable entry must win on a full tie")
	}
	if wins(base, base) {
		t.Error("a non-executable entry doesn't win over itself")
	}
}

func TestConflictPath(t *testing.T) {
	for _, tc := range []struct{ orig, hash, want string }{
		{"a.md", "abcd1234ef", "a.synk-conflict-abcd1234.md"},
		{"notes/a.md", "abcd1234", "notes/a.synk-conflict-abcd1234.md"},
		{"archive.tar", "0123456789", "archive.synk-conflict-01234567.tar"},
		{"noext", "abcd1234", "noext.synk-conflict-abcd1234"},
	} {
		if got := ConflictPath(tc.orig, tc.hash); got != tc.want {
			t.Errorf("ConflictPath(%q, %q) = %q; want %q", tc.orig, tc.hash, got, tc.want)
		}
	}
}

func TestIsConflictName(t *testing.T) {
	for path, want := range map[string]bool{
		"a.synk-conflict-abcd1234.md":   true,
		"notes/b.synk-conflict-1234.md": true,
		"a.md":                          false,
		"synk-conflict.md":              false,
	} {
		if got := IsConflictName(path); got != want {
			t.Errorf("IsConflictName(%q) = %v; want %v", path, got, want)
		}
	}
}
