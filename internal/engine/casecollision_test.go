package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
)

// libraryAt enables a library on n in the folder dir.
func libraryAt(t *testing.T, n *Node, id, dir string) string {
	t.Helper()
	must(t, n.EnableLibrary(config.Library{ID: id, Preset: "custom", Path: dir}))
	return dir
}

// On a file system that folds case, a peer's two spellings of one name both
// arrive: one at its own path, one at its conflict name. Neither replaces
// the other, and the next sync is quiet.
func TestCaseCollisionKeepsBothVersions(t *testing.T) {
	a, b := openNode(t, "Case-sensitive Mac"), openNode(t, "Mac")
	must(t, Pair(a, b))
	addrA := listen(t, a)

	// The sender's library has to hold both spellings, which needs a
	// case-sensitive volume; the test skips where the volume folds them.
	dirA := filepath.Join(t.TempDir(), "notes")
	if cs := os.Getenv("SYNK_CASE_SENSITIVE_DIR"); cs != "" {
		dirA = filepath.Join(cs, "notes")
	}
	must(t, os.RemoveAll(dirA))
	must(t, os.MkdirAll(dirA, 0o700))
	libraryAt(t, a, "notes", dirA)
	writeFile(t, dirA, "plain.md", "plain")
	writeFile(t, dirA, "Note.md", "upper")
	if err := os.WriteFile(filepath.Join(dirA, "note.md"), []byte("lower"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dirA, "Note.md")); string(got) != "upper" {
		t.Skip("the file system holding the sender's library folds case")
	}

	dirB := library(t, b, "notes")
	b.FoldProbe = func(string) bool { return true }

	must(t, b.SyncWith(addrA, "notes"))

	lower := atomicfile.HashBytes([]byte("lower"))
	// The keeper is the path that sorts first when nothing is held locally,
	// so the loser's copy borrows the keeper's spelling.
	conflict := reconcile.ConflictPath("Note.md", lower)
	wantFile(t, dirB, "Note.md", "upper")
	wantFile(t, dirB, conflict, "lower")
	wantFile(t, dirB, "plain.md", "plain")

	// The sender keeps both of its files, whatever the receiver did.
	wantFile(t, dirA, "Note.md", "upper")
	wantFile(t, dirA, "note.md", "lower")

	// Converged: syncing again changes nothing and doesn't spin.
	must(t, SyncOnce(a, b, "notes"))
	must(t, SyncOnce(a, b, "notes"))

	// The receiver's extra copy reaches the sender as an ordinary file,
	// beside the two originals.
	wantFile(t, dirA, conflict, "lower")
}

// A version that folds onto a local file the index already holds is a
// concurrent edit of that file, so the normal conflict flow applies: the
// winner at the local path, the loser beside it.
func TestCaseDriftConflictsLikeAnEdit(t *testing.T) {
	a, b := openNode(t, "Case-sensitive Mac"), openNode(t, "Mac")
	must(t, Pair(a, b))
	addrA := listen(t, a)

	dirA := filepath.Join(t.TempDir(), "notes")
	if cs := os.Getenv("SYNK_CASE_SENSITIVE_DIR"); cs != "" {
		// When a case-sensitive volume is available, run the sender on it so
		// the test also covers the mixed case: one folding side, one not.
		dirA = filepath.Join(cs, "drift-notes")
	}
	must(t, os.RemoveAll(dirA))
	must(t, os.MkdirAll(dirA, 0o700))
	libraryAt(t, a, "notes", dirA)
	writeFile(t, dirA, "note.md", "remote edit")

	dirB := library(t, b, "notes")
	b.FoldProbe = func(string) bool { return true }
	writeFile(t, dirB, "Note.md", "local edit")

	must(t, b.SyncWith(addrA, "notes"))

	// Both edits survive; which one sits at Note.md is the usual
	// deterministic conflict choice, and the other is a conflict copy.
	lower := atomicfile.HashBytes([]byte("remote edit"))
	upper := atomicfile.HashBytes([]byte("local edit"))
	localNewer := localWins(t, dirB)
	if localNewer {
		wantFile(t, dirB, "Note.md", "local edit")
		wantFile(t, dirB, reconcile.ConflictPath("Note.md", lower), "remote edit")
	} else {
		wantFile(t, dirB, "Note.md", "remote edit")
		wantFile(t, dirB, reconcile.ConflictPath("Note.md", upper), "local edit")
	}
	// Converged: syncing again changes nothing and doesn't spin.
	must(t, SyncOnce(a, b, "notes"))
	must(t, SyncOnce(a, b, "notes"))
}

// localWins answers whether the receiver's own file won the conflict at
// path, by comparing the file's content with what each side wrote.
func localWins(t *testing.T, dir string) bool {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, "Note.md"))
	must(t, err)
	return string(got) == "local edit"
}
