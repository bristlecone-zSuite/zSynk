package reconcile

import (
	"slices"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

func TestAdvanceCreatesTombstone(t *testing.T) {
	prev := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)})
	next, deleted := Advance(prev, "A", nil, AdvanceOptions{})
	e, ok := next.Get("a.md")
	if !ok || !e.Deleted {
		t.Fatalf("%+v", e)
	}
	if e.Vector.Get("A") != 2 {
		t.Fatalf("vector %v", e.Vector)
	}
	if !slices.Equal(deleted, []string{"a.md"}) {
		t.Fatalf("deleted %v", deleted)
	}
}

func TestAdvanceNoTombstoneForIncomplete(t *testing.T) {
	prev := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)})
	next, deleted := Advance(prev, "A", []ScanItem{{Path: "a.md", Kind: KindFile, Incomplete: true}}, AdvanceOptions{})
	e, ok := next.Get("a.md")
	if !ok || e.Deleted || !e.Incomplete || e.Hash != "h1" || len(deleted) > 0 {
		t.Fatalf("a placeholder became %+v", e)
	}
}

func TestAdvanceDeletesAnIncompleteEntryOnlyWhenConfirmedGone(t *testing.T) {
	prev := idx("A",
		Entry{Path: "was-busy.md", Kind: KindFile, Hash: "h1", Incomplete: true, Vector: vectorMap("A", 1)},
		Entry{Path: "never-had.md", Kind: KindFile, Incomplete: true},
	)
	next, deleted := Advance(prev, "A", nil, AdvanceOptions{})
	if e := next.Entries["was-busy.md"]; !e.Deleted || e.Incomplete || e.Vector.Get("A") != 2 {
		t.Fatalf("was-busy.md: %+v", e)
	}
	if _, ok := next.Entries["never-had.md"]; ok {
		t.Fatal("kept an entry this device never had a version of")
	}
	if !slices.Equal(deleted, []string{"was-busy.md"}) {
		t.Fatalf("deleted %v", deleted)
	}
}

func TestAdvanceBumpOnChange(t *testing.T) {
	prev := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)})
	next, _ := Advance(prev, "A", []ScanItem{{Path: "a.md", Kind: KindFile, Hash: "h2", Size: 2}}, AdvanceOptions{})
	e, _ := next.Get("a.md")
	if e.Hash != "h2" || e.Vector.Get("A") != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestAdvanceRecreatedFileWinsOverItsTombstone(t *testing.T) {
	tomb := Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Deleted: true, Vector: vector.Vector{"A": 2, "B": 1}, Meta: Meta{MTime: 100, By: "B"}}
	next, _ := Advance(idx("A", tomb), "A", []ScanItem{{Path: "a.md", Kind: KindFile, Hash: "h2", Exec: true, MTime: 200}}, AdvanceOptions{})
	e := next.Entries["a.md"]
	if e.Deleted || e.Hash != "h2" || !vector.Dominates(e.Vector, tomb.Vector) || e.Meta != (Meta{Exec: true, MTime: 200, By: "A"}) {
		t.Fatalf("%+v", e)
	}
}

func TestAdvanceRecordsWhoMadeEachVersionAndWhen(t *testing.T) {
	theirs := Meta{MTime: 100, By: "B"}
	prev := idx("A",
		Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("B", 1), Meta: theirs},
		Entry{Path: "gone.md", Kind: KindFile, Hash: "h2", Vector: vectorMap("B", 1), Meta: theirs},
	)
	scan := []ScanItem{
		{Path: "a.md", Kind: KindFile, Hash: "h1", MTime: 150},
		{Path: "new.sh", Kind: KindFile, Hash: "h3", Exec: true, MTime: 200},
	}
	next, _ := Advance(prev, "A", scan, AdvanceOptions{})
	if e := next.Entries["a.md"]; e.Meta != theirs || !vector.Equal(e.Vector, vectorMap("B", 1)) {
		t.Errorf("a touch made a version: %+v", e)
	}
	if e := next.Entries["new.sh"]; e.Meta != (Meta{Exec: true, MTime: 200, By: "A"}) {
		t.Errorf("new file: %+v", e)
	}
	if e := next.Entries["gone.md"]; !e.Deleted || e.By != "A" || e.MTime != 100 {
		t.Errorf("tombstone: %+v", e)
	}

	scan = []ScanItem{{Path: "a.md", Kind: KindFile, Hash: "h1", Exec: true, MTime: 150}}
	next, _ = Advance(prev, "A", scan, AdvanceOptions{})
	if e := next.Entries["a.md"]; e.Meta != (Meta{Exec: true, MTime: 150, By: "A"}) || e.Vector.Get("A") != 1 {
		t.Errorf("made executable: %+v", e)
	}
	scan = []ScanItem{{Path: "a.md", Kind: KindFile, Hash: "h4", MTime: 300}}
	next, _ = Advance(prev, "A", scan, AdvanceOptions{})
	if e := next.Entries["a.md"]; e.Meta != (Meta{MTime: 300, By: "A"}) || e.Vector.Get("A") != 1 {
		t.Errorf("edited: %+v", e)
	}
}

func TestAdvanceIgnoresExecutableBitsTheFileSystemDoesNotKeep(t *testing.T) {
	prev := idx("A",
		Entry{Path: "plain.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)},
		Entry{Path: "run.sh", Kind: KindFile, Hash: "h2", Vector: vectorMap("A", 1), Meta: Meta{Exec: true}},
	)
	scan := []ScanItem{
		{Path: "plain.md", Kind: KindFile, Hash: "h1", Exec: true},
		{Path: "run.sh", Kind: KindFile, Hash: "h2"},
		{Path: "new.md", Kind: KindFile, Hash: "h3", Exec: true},
	}
	next, _ := Advance(prev, "A", scan, AdvanceOptions{NoExec: true})
	for p, exec := range map[string]bool{"plain.md": false, "run.sh": true, "new.md": false} {
		if e := next.Entries[p]; e.Exec != exec || e.Vector.Get("A") != 1 {
			t.Errorf("%s: %+v", p, e)
		}
	}
	next, _ = Advance(next, "A", []ScanItem{{Path: "run.sh", Kind: KindFile, Hash: "h4"}}, AdvanceOptions{NoExec: true})
	if e := next.Entries["run.sh"]; !e.Exec || e.Hash != "h4" || e.Vector.Get("A") != 2 {
		t.Errorf("edited: %+v", e)
	}
	next, _ = Advance(next, "A", []ScanItem{{Path: "run.sh", Kind: KindPackage, Hash: "h5"}}, AdvanceOptions{NoExec: true})
	if e := next.Entries["run.sh"]; e.Exec || e.Kind != KindPackage {
		t.Errorf("replaced by a package: %+v", e)
	}
}

func TestAdvanceUnchangedKeepsVector(t *testing.T) {
	prev := idx("A", Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 3)})
	next, _ := Advance(prev, "A", []ScanItem{{Path: "a.md", Kind: KindFile, Hash: "h1"}}, AdvanceOptions{})
	e, _ := next.Get("a.md")
	if e.Vector.Get("A") != 3 {
		t.Fatalf("%+v", e)
	}
}

func TestAdvanceKeepsWhatTheScanCouldNotSee(t *testing.T) {
	prev := idx("A",
		Entry{Path: "Projects/plan.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)},
		Entry{Path: "Projects/sub/x.md", Kind: KindFile, Hash: "h2", Vector: vectorMap("A", 1)},
		Entry{Path: "Projectsy.md", Kind: KindFile, Hash: "h3", Vector: vectorMap("A", 1)},
		Entry{Path: "link.md", Kind: KindFile, Hash: "h4", Vector: vectorMap("A", 1)},
	)
	next, deleted := Advance(prev, "A", nil, AdvanceOptions{Unconfirmed: []string{"Projects", "link.md"}})
	for _, p := range []string{"Projects/plan.md", "Projects/sub/x.md", "link.md"} {
		if e := next.Entries[p]; e.Deleted || !e.Incomplete || e.Hash == "" {
			t.Fatalf("%s: %+v", p, e)
		}
	}
	if !slices.Equal(deleted, []string{"Projectsy.md"}) {
		t.Fatalf("deleted %v", deleted)
	}

	next, deleted = Advance(prev, "A", nil, AdvanceOptions{Unconfirmed: []string{"."}})
	if len(deleted) > 0 || !next.Entries["Projectsy.md"].Incomplete {
		t.Fatalf("an unreadable root deleted %v", deleted)
	}
}

func TestAdvanceKeepsIgnoredPathsWithoutATombstone(t *testing.T) {
	const ws = ".obsidian/workspace.json"
	prev := idx("A",
		Entry{Path: ws, Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)},
		Entry{Path: "a.md", Kind: KindFile, Hash: "h2", Vector: vectorMap("A", 1)},
	)
	ignored := func(p string, isDir bool) bool { return strings.HasPrefix(p, ".obsidian/") }
	scan := []ScanItem{
		{Path: "a.md", Kind: KindFile, Hash: "h2"},
		{Path: ".obsidian/cache/x", Kind: KindFile, Hash: "h3"},
	}
	next, deleted := Advance(prev, "A", scan, AdvanceOptions{Ignored: ignored})
	if len(deleted) > 0 || !slices.Equal(SortedPaths(next), []string{ws, "a.md"}) {
		t.Fatalf("deleted %v, entries %v", deleted, SortedPaths(next))
	}
	if e := next.Entries[ws]; e.Deleted || !e.Incomplete || e.Hash != "h1" {
		t.Fatalf("ignored entry %+v", e)
	}

	// Edited while ignored, then no longer ignored: the edit is a new
	// version of the old history, not an unrelated file a peer's older
	// copy would replace.
	next, _ = Advance(next, "A", []ScanItem{{Path: ws, Kind: KindFile, Hash: "h9"}, {Path: "a.md", Kind: KindFile, Hash: "h2"}}, AdvanceOptions{})
	if e := next.Entries[ws]; e.Incomplete || e.Hash != "h9" || e.Vector.Get("A") != 2 {
		t.Fatalf("after un-ignoring: %+v", e)
	}
}

func TestAdvanceHoldsDeletes(t *testing.T) {
	prev := idx("A",
		Entry{Path: "a.md", Kind: KindFile, Hash: "h1", Vector: vectorMap("A", 1)},
		Entry{Path: "b.md", Kind: KindFile, Hash: "h2", Vector: vectorMap("A", 1)},
	)
	next, deleted := Advance(prev, "A", nil, AdvanceOptions{Hold: map[string]bool{"a.md": true}})
	if e := next.Entries["a.md"]; e.Deleted || e.Vector.Get("A") != 1 {
		t.Fatalf("held entry %+v", e)
	}
	if !slices.Equal(deleted, []string{"b.md"}) {
		t.Fatalf("deleted %v", deleted)
	}
}

func vectorMap(id string, n uint64) map[string]uint64 {
	return map[string]uint64{id: n}
}
