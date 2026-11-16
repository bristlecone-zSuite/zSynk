package engine

import (
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
)

func entry(path, hash string, vector ...string) reconcile.Entry {
	e := reconcile.Entry{Path: path, Kind: reconcile.KindFile, Hash: hash, Size: 1}
	e.Vector = e.Vector.Bump("A")
	for _, v := range vector {
		e.Vector = e.Vector.Bump(v)
	}
	return e
}

func index(folder string, entries ...reconcile.Entry) *reconcile.Index {
	idx := reconcile.NewIndex(folder, "A")
	for _, e := range entries {
		idx.Entries[e.Path] = e
	}
	return idx
}

// A remote path that folds onto a local file is compared as a version of
// that file, not as a new one.
func TestFoldAdjustAliasesOneRemotePathToLocal(t *testing.T) {
	local := index("notes", entry("Note.md", "h1"))
	remote := index("notes", entry("note.md", "h2"))
	got := foldAdjust(local, remote)
	if e, ok := got.Entries["Note.md"]; !ok || e.Hash != "h2" {
		t.Fatalf("remote should be aliased to Note.md, got %+v", got.Entries)
	}
	if _, ok := got.Entries["note.md"]; ok {
		t.Fatalf("the original spelling should be gone: %+v", got.Entries)
	}
}

// A remote tombstone of a folded spelling deletes the file it names here.
func TestFoldAdjustAliasesRemoteTombstone(t *testing.T) {
	local := index("notes", entry("Note.md", "h1"))
	deleted := entry("note.md", "h1")
	deleted.Deleted = true
	deleted.Vector = deleted.Vector.Bump("B") // dominates the local version
	remote := index("notes", deleted)
	got := foldAdjust(local, remote)
	if _, ok := got.Entries["note.md"]; ok {
		t.Fatalf("the tombstone should be aliased: %+v", got.Entries)
	}
	if e, ok := got.Entries["Note.md"]; !ok || !e.Deleted {
		t.Fatalf("Note.md should carry the delete: %+v", got.Entries["Note.md"])
	}
}

// Two remote spellings with different content both arrive: one at its own
// path, the other at its conflict name.
func TestFoldAdjustRewritesCollidingRemotePaths(t *testing.T) {
	remote := index("notes", entry("Note.md", "h1"), entry("note.md", "h2"))
	got := foldAdjust(reconcile.NewIndex("notes", "A"), remote)
	if _, ok := got.Entries["note.md"]; ok {
		t.Fatalf("note.md can't be held here and must move: %+v", got.Entries)
	}
	want := reconcile.ConflictPath("Note.md", "h2")
	if e, ok := got.Entries[want]; !ok || e.Hash != "h2" {
		t.Fatalf("note.md's content should be at %s: %+v", want, got.Entries)
	}
	if e, ok := got.Entries["Note.md"]; !ok || e.Hash != "h1" {
		t.Fatalf("Note.md should keep its content: %+v", got.Entries)
	}
}

// A path a local file already holds keeps its name, and the remote
// spellings that fold onto it move aside.
func TestFoldAdjustKeepsTheLocalPath(t *testing.T) {
	local := index("notes", entry("note.md", "h1"))
	remote := index("notes", entry("Note.md", "h2"), entry("note.md", "h1"))
	got := foldAdjust(local, remote)
	want := reconcile.ConflictPath("note.md", "h2")
	if e, ok := got.Entries[want]; !ok || e.Hash != "h2" {
		t.Fatalf("Note.md's content should be at %s: %+v", want, got.Entries)
	}
	if e, ok := got.Entries["note.md"]; !ok || e.Hash != "h1" {
		t.Fatalf("note.md should stay the version the local file holds: %+v", got.Entries)
	}
}

// Once a colliding version has been copied to its conflict name, the same
// remote index produces no further entry for the original spelling, so the
// sync is quiet.
func TestFoldAdjustIsQuietOnceTheCopyExists(t *testing.T) {
	local := index("notes", entry("Note.md", "h1"), entry(reconcile.ConflictPath("note.md", "h2"), "h2"))
	remote := index("notes", entry("Note.md", "h1"), entry("note.md", "h2"))
	got := foldAdjust(local, remote)
	if _, ok := got.Entries["note.md"]; ok {
		t.Fatalf("note.md should be dropped, its copy is in the index: %+v", got.Entries)
	}
}

// The same input always rewrites the same way.
func TestFoldAdjustIsDeterministic(t *testing.T) {
	local := index("notes", entry("note.md", "h1"))
	remote := index("notes", entry("Note.md", "h2"), entry("note.md", "h1"), entry("NOTES.md", "h3"))
	first := foldAdjust(local, remote)
	second := foldAdjust(local, remote)
	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("different sizes: %d and %d", len(first.Entries), len(second.Entries))
	}
	for k, e := range first.Entries {
		o, ok := second.Entries[k]
		if !ok || o.Hash != e.Hash || o.Path != e.Path {
			t.Fatalf("%s differs: %+v and %+v", k, e, o)
		}
	}
}
