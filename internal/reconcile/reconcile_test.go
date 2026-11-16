package reconcile

import (
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

func idx(id string, entries ...Entry) *Index {
	i := NewIndex("lib", id)
	for _, e := range entries {
		if e.Vector == nil {
			e.Vector = vector.Vector{}
		}
		i.Entries[e.Path] = e
	}
	return i
}

func TestRemoteCreate(t *testing.T) {
	local := idx("A")
	remote := idx("B", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 1 || ops[0].Kind != OpSet || ops[0].Hash != "h1" {
		t.Fatalf("%+v", ops)
	}
}

func TestLocalOnlyNotDeleted(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1}})
	remote := idx("B")
	ops := Reconcile(local, remote)
	if len(ops) != 0 {
		t.Fatalf("must not delete unseen files: %+v", ops)
	}
}

func TestRemoteDominates(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1}})
	remote := idx("B", Entry{Path: "a.md", Kind: KindFile, Hash: "h2", Vector: vector.Vector{"A": 1, "B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 1 || ops[0].Kind != OpSet || ops[0].Hash != "h2" {
		t.Fatalf("%+v", ops)
	}
}

func TestLocalDominates(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h2", Vector: vector.Vector{"A": 2, "B": 1}})
	remote := idx("B", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1, "B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 0 {
		t.Fatalf("%+v", ops)
	}
}

func TestConcurrentConflictDeterministic(t *testing.T) {
	h1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	h2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	local := idx("A", Entry{Path: "Essay.md", Kind: KindFile, Hash: h1, Vector: vector.Vector{"A": 2, "B": 1}})
	remote := idx("B", Entry{Path: "Essay.md", Kind: KindFile, Hash: h2, Vector: vector.Vector{"A": 1, "B": 2}})
	opsA := Reconcile(local, remote)
	opsB := Reconcile(remote, local)
	if len(opsA) != 1 || opsA[0].Kind != OpConflict {
		t.Fatalf("A: %+v", opsA)
	}
	if len(opsB) != 1 || opsB[0].Kind != OpConflict {
		t.Fatalf("B: %+v", opsB)
	}
	if opsA[0].Hash != opsB[0].Hash {
		t.Fatalf("winner mismatch %s vs %s", opsA[0].Hash, opsB[0].Hash)
	}
	if opsA[0].Hash != h1 { // h1 < h2 so h1 wins original path
		t.Fatalf("expected lexicographic winner h1, got %s", opsA[0].Hash)
	}
	if opsA[0].ConflictHash != h2 || opsB[0].ConflictHash != h2 {
		t.Fatalf("loser should be h2: A=%s B=%s", opsA[0].ConflictHash, opsB[0].ConflictHash)
	}
	if opsA[0].ConflictPath != opsB[0].ConflictPath {
		t.Fatal("conflict path must match")
	}
	if !IsConflictName(opsA[0].ConflictPath) {
		t.Fatal(opsA[0].ConflictPath)
	}
}

func TestConflictKeepsTheLaterEditAtThePath(t *testing.T) {
	// The older edit has the smaller hash, which used to decide.
	older := Entry{Path: "Essay.md", Kind: KindFile, Hash: "aaaa1111", Size: 1, Vector: vector.Vector{"A": 2, "B": 1}, Meta: Meta{MTime: 100, By: "A"}}
	newer := Entry{Path: "Essay.md", Kind: KindFile, Hash: "bbbb2222", Size: 2, Vector: vector.Vector{"A": 1, "B": 2}, Meta: Meta{Exec: true, MTime: 200, By: "B"}}
	for _, c := range []struct {
		at            string
		local, remote Entry
	}{{"A", older, newer}, {"B", newer, older}} {
		ops := Reconcile(idx(c.at, c.local), idx("other", c.remote))
		if len(ops) != 1 || ops[0].Kind != OpConflict {
			t.Fatalf("at %s: %+v", c.at, ops)
		}
		op := ops[0]
		if op.Hash != newer.Hash || op.Size != newer.Size || op.Meta != newer.Meta {
			t.Errorf("at %s, the later edit isn't at the path: %+v", c.at, op)
		}
		if op.ConflictHash != older.Hash || op.ConflictSize != older.Size || op.ConflictMeta != older.Meta || op.ConflictPath != ConflictPath("Essay.md", older.Hash) {
			t.Errorf("at %s, the conflict copy isn't the older edit: %+v", c.at, op)
		}
		if !vector.Equal(op.Vector, vector.Vector{"A": 2, "B": 2}) {
			t.Errorf("at %s: vector %v", c.at, op.Vector)
		}
	}
}

func TestWinsIsATotalOrder(t *testing.T) {
	base := Entry{Kind: KindFile, Hash: "bbbb", Meta: Meta{MTime: 100, By: "B"}}
	with := func(change func(*Entry)) Entry {
		e := base
		change(&e)
		return e
	}
	// Each winner loses on every rule after the one that decides.
	for _, c := range []struct {
		name          string
		winner, loser Entry
	}{
		{"the later edit", with(func(e *Entry) { e.MTime, e.By, e.Hash, e.Kind = 101, "A", "cccc", KindPackage }), base},
		{"the device that sorts last", with(func(e *Entry) { e.By, e.Hash, e.Kind = "C", "cccc", KindPackage }), base},
		{"the smaller hash", with(func(e *Entry) { e.Hash, e.Kind = "aaaa", KindPackage }), base},
		{"a file over a package", base, with(func(e *Entry) { e.Kind, e.Exec = KindPackage, true })},
		{"the executable one", with(func(e *Entry) { e.Exec = true }), base},
	} {
		if !wins(c.winner, c.loser) || wins(c.loser, c.winner) {
			t.Errorf("%s: wins(winner, loser) = %v, wins(loser, winner) = %v", c.name, wins(c.winner, c.loser), wins(c.loser, c.winner))
		}
	}
	exec := with(func(e *Entry) { e.Exec = true })
	if wins(base, base) || wins(exec, exec) {
		t.Error("a version wins over itself")
	}
}

func TestOnlyTheExecutableBitChanged(t *testing.T) {
	plain := Entry{Path: "run.sh", Kind: KindFile, Hash: "h1", Size: 3, Vector: vector.Vector{"A": 1}, Meta: Meta{MTime: 100, By: "A"}}
	exec := plain
	exec.Vector = vector.Vector{"A": 1, "B": 1}
	exec.Meta = Meta{Exec: true, MTime: 100, By: "B"}

	ops := Reconcile(idx("A", plain), idx("B", exec))
	if len(ops) != 1 || ops[0].Kind != OpSet || ops[0].Hash != "h1" || ops[0].Meta != exec.Meta || !vector.Equal(ops[0].Vector, exec.Vector) {
		t.Fatalf("made executable since: %+v", ops)
	}
	if ops := Reconcile(idx("B", exec), idx("A", plain)); len(ops) != 0 {
		t.Fatalf("an older version: %+v", ops)
	}

	// Concurrent versions with the same content: the winner's bit, on
	// both sides, and no conflict copy.
	plain.Vector = vector.Vector{"A": 2}
	plain.MTime = 200
	merged := vector.Vector{"A": 2, "B": 1}
	atA := Reconcile(idx("A", plain), idx("B", exec))
	if len(atA) != 1 || atA[0].Kind != OpMergeVector || !vector.Equal(atA[0].Vector, merged) {
		t.Fatalf("where the winner is: %+v", atA)
	}
	atB := Reconcile(idx("B", exec), idx("A", plain))
	if len(atB) != 1 || atB[0].Kind != OpSet || atB[0].Hash != "h1" || atB[0].Meta != plain.Meta || !vector.Equal(atB[0].Vector, merged) {
		t.Fatalf("where the loser is: %+v", atB)
	}

	// The same version with different bits, which no peer should send,
	// still settles on one, without an op that changes nothing.
	exec.Vector = plain.Vector
	if ops := Reconcile(idx("A", plain), idx("B", exec)); len(ops) != 0 {
		t.Fatalf("where the winner is: %+v", ops)
	}
	if ops := Reconcile(idx("B", exec), idx("A", plain)); len(ops) != 1 || ops[0].Kind != OpSet || ops[0].Meta != plain.Meta {
		t.Fatalf("where the loser is: %+v", ops)
	}
}

func TestOpsCarryTheVersionsMeta(t *testing.T) {
	m := Meta{Exec: true, MTime: 100, By: "B"}
	theirs := Entry{Path: "a.sh", Kind: KindFile, Hash: "h2", Vector: vector.Vector{"A": 1, "B": 1}, Meta: m}
	ours := Entry{Path: "a.sh", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1}}
	deleted := ours
	deleted.Deleted = true
	for name, local := range map[string]*Index{"new": idx("A"), "deleted here": idx("A", deleted), "older here": idx("A", ours)} {
		if ops := Reconcile(local, idx("B", theirs)); len(ops) != 1 || ops[0].Kind != OpSet || ops[0].Meta != m {
			t.Errorf("%s: %+v", name, ops)
		}
	}
	theirs.Deleted = true
	if ops := Reconcile(idx("A", ours), idx("B", theirs)); len(ops) != 1 || ops[0].Kind != OpDelete || ops[0].Meta != m {
		t.Errorf("deleted there: %+v", ops)
	}
}

func TestDeleteDominates(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1}})
	remote := idx("B", Entry{Path: "a.md", Deleted: true, Vector: vector.Vector{"A": 1, "B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 1 || ops[0].Kind != OpDelete {
		t.Fatalf("%+v", ops)
	}
}

func TestConcurrentDeleteLosesToEdit(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h2", Vector: vector.Vector{"A": 2}})
	remote := idx("B", Entry{Path: "a.md", Deleted: true, Vector: vector.Vector{"A": 1, "B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 1 || ops[0].Kind != OpMergeVector || ops[0].Hash != "h2" {
		t.Fatalf("edit must win concurrent delete: %+v", ops)
	}
}

func TestTombstoneDoesNotResurrectWhenDominating(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Deleted: true, Vector: vector.Vector{"A": 2, "B": 1}})
	remote := idx("B", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1, "B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 0 {
		t.Fatalf("%+v", ops)
	}
}

func TestIncompleteNeverDeletes(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1}})
	remote := idx("B", Entry{Path: "a.md", Incomplete: true, Vector: vector.Vector{"B": 1}})
	ops := Reconcile(local, remote)
	if len(ops) != 0 {
		t.Fatalf("%+v", ops)
	}
}

func TestIdenticalHashMergesVector(t *testing.T) {
	local := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"A": 1}})
	remote := idx("B", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vector.Vector{"B": 4}})
	ops := Reconcile(local, remote)
	if len(ops) != 1 || ops[0].Kind != OpMergeVector {
		t.Fatalf("%+v", ops)
	}
	if ops[0].Vector.Get("A") != 1 || ops[0].Vector.Get("B") != 4 {
		t.Fatalf("%v", ops[0].Vector)
	}
}
