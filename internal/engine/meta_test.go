package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

// The executable bit syncs either way, and so does the time a file or
// package was modified, which apps sort by. A touch alone doesn't sync.
func TestExecutableBitsAndTimesSync(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	when := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	writeFile(t, dirA, "run.sh", "#!/bin/sh\n")
	must(t, os.Chmod(filepath.Join(dirA, "run.sh"), 0o755))
	writeFile(t, dirA, "Talk.key/Index.zip", "idx")
	for _, p := range []string{"run.sh", "Talk.key/Index.zip", "Talk.key"} {
		touch(t, dirA, p, when)
	}
	must(t, SyncOnce(a, b, "notes"))
	wantMeta(t, dirB, "run.sh", 0o700, when)
	wantMeta(t, dirB, "Talk.key", fs.ModeDir|0o700, when)

	must(t, os.Chmod(filepath.Join(dirB, "run.sh"), 0o600))
	must(t, SyncOnce(a, b, "notes"))
	wantMeta(t, dirA, "run.sh", 0o644, when)

	v := a.Index("notes").Entries["run.sh"].Vector
	touch(t, dirA, "run.sh", when.Add(time.Hour))
	must(t, SyncOnce(a, b, "notes"))
	if got := a.Index("notes").Entries["run.sh"].Vector; !vector.Equal(got, v) {
		t.Errorf("a touch made a version: %v, was %v", got, v)
	}
	wantMeta(t, dirB, "run.sh", 0o600, when)
}

// FAT and exFAT drives show every file as executable. A library on one
// sends no executable bits, and keeps those it gets.
func TestLibrariesWithoutExecutableBitsDontSendThem(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	t.Cleanup(func() { execBits = fsmeta.ExecBits })
	execBits = func(dir string) bool { return dir != dirA }
	when := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	writeFile(t, dirA, "notes.md", "hi")
	must(t, os.Chmod(filepath.Join(dirA, "notes.md"), 0o755))
	touch(t, dirA, "notes.md", when)
	must(t, SyncOnce(a, b, "notes"))
	wantMeta(t, dirB, "notes.md", 0o600, when)

	must(t, os.Chmod(filepath.Join(dirB, "notes.md"), 0o700))
	must(t, SyncOnce(a, b, "notes"))
	if e := a.Index("notes").Entries["notes.md"]; !e.Exec {
		t.Errorf("didn't keep the bit it got: %+v", e)
	}
}

// When both devices edit a file before they sync, the later edit stays at
// the path and the other goes to a conflict copy, whichever device made
// which.
func TestConflictKeepsTheLaterEditAtThePath(t *testing.T) {
	earlier := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	for later, other := range map[string]string{"A": "B", "B": "A"} {
		t.Run("later on "+later, func(t *testing.T) {
			a, b, dirA, dirB := pairedLibrary(t)
			dirs := map[string]string{"A": dirA, "B": dirB}
			writeFile(t, dirA, "essay.md", "draft")
			must(t, SyncOnce(a, b, "notes"))
			writeFile(t, dirs[other], "essay.md", "from "+other)
			touch(t, dirs[other], "essay.md", earlier)
			writeFile(t, dirs[later], "essay.md", "from "+later)
			touch(t, dirs[later], "essay.md", earlier.Add(time.Minute))
			must(t, SyncOnce(a, b, "notes"))
			conflict := reconcile.ConflictPath("essay.md", atomicfile.HashBytes([]byte("from "+other)))
			for _, dir := range dirs {
				wantFile(t, dir, "essay.md", "from "+later)
				wantMeta(t, dir, "essay.md", 0o600, earlier.Add(time.Minute))
				wantFile(t, dir, conflict, "from "+other)
				wantMeta(t, dir, conflict, 0o600, earlier)
			}
		})
	}
}

// The vault keeps content only, so a restored file is executable if it is
// now, or was when it was deleted.
func TestRestoredFilesKeepTheirExecutableBit(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "run.sh", "v1")
	must(t, os.Chmod(filepath.Join(dirA, "run.sh"), 0o755))
	must(t, SyncOnce(a, b, "notes"))
	writeFile(t, dirB, "run.sh", "v2")
	must(t, SyncOnce(a, b, "notes"))
	_, err := a.RestoreVersion("notes", "run.sh", "")
	must(t, err)
	wantFile(t, dirA, "run.sh", "v1")
	wantExec(t, dirA, "run.sh")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "run.sh", "v1")
	wantExec(t, dirB, "run.sh")

	must(t, os.Remove(filepath.Join(dirB, "run.sh")))
	must(t, SyncOnce(a, b, "notes"))
	wantGone(t, dirA, "run.sh")
	_, err = a.RestoreVersion("notes", "run.sh", "")
	must(t, err)
	wantExec(t, dirA, "run.sh")
	must(t, SyncOnce(a, b, "notes"))
	wantExec(t, dirB, "run.sh")
}

// Putting back held deletes puts back what the files were, not just their
// content.
func TestRestoredDeletesKeepTheirMeta(t *testing.T) {
	a, b, dirA, _ := pairedLibrary(t)
	writeNotes(t, dirA, 0, 20)
	must(t, os.Chmod(filepath.Join(dirA, note(0)), 0o755))
	when := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	touch(t, dirA, note(0), when)
	must(t, SyncOnce(a, b, "notes"))
	removeNotes(t, dirA, 0, 15)
	_, err := a.ScanLibrary("notes")
	must(t, err)
	_, err = a.RestoreDeletes("notes")
	must(t, err)
	wantMeta(t, dirA, note(0), 0o700, when)
}

func TestCheckRemoteMeta(t *testing.T) {
	h := atomicfile.HashBytes([]byte("x"))
	idx := reconcile.NewIndex("notes", "PEER")
	v := vector.Vector{"PEER": 1}
	idx.Entries["a.sh"] = reconcile.Entry{Path: "a.sh", Kind: reconcile.KindFile, Hash: h, Vector: v, Meta: reconcile.Meta{Exec: true, By: "PEER"}}
	idx.Entries["Talk.key"] = reconcile.Entry{Path: "Talk.key", Kind: reconcile.KindPackage, Hash: h, Vector: v, Meta: reconcile.Meta{Exec: true}}
	idx.Entries["by.md"] = reconcile.Entry{Path: "by.md", Kind: reconcile.KindFile, Hash: h, Vector: v, Meta: reconcile.Meta{By: string(make([]byte, maxDeviceIDLen+1))}}
	got, dropped, err := checkRemote(idx, "notes")
	must(t, err)
	if len(dropped) != 1 || len(got.Entries) != 2 {
		t.Fatalf("kept %v, dropped %v", got.Entries, dropped)
	}
	if !got.Entries["a.sh"].Exec || got.Entries["Talk.key"].Exec {
		t.Errorf("a file %+v, a package %+v", got.Entries["a.sh"], got.Entries["Talk.key"])
	}
}

func touch(t *testing.T, dir, rel string, when time.Time) {
	t.Helper()
	must(t, os.Chtimes(filepath.Join(dir, filepath.FromSlash(rel)), time.Time{}, when))
}

func wantMeta(t *testing.T, dir, rel string, mode fs.FileMode, modified time.Time) {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil || st.Mode() != mode || !st.ModTime().Equal(modified) {
		t.Errorf("%s: %v; want mode %v, modified %v (%v)", rel, st, mode, modified, err)
		if st != nil {
			t.Logf("%s has mode %v, modified %v", rel, st.Mode(), st.ModTime())
		}
	}
}

func wantExec(t *testing.T, dir, rel string) {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil || st.Mode()&0o100 == 0 {
		t.Errorf("%s isn't executable: %v", rel, err)
	}
}
