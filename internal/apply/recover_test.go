package apply

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/journal"
)

const txA = "tx-00000000000000a1"

// interrupted journals a transaction that stopped after steps, as a crash
// would leave it, and returns its staging directory.
func (f *fixture) interrupted(tx, op, rel string, steps ...journal.Status) string {
	f.t.Helper()
	for _, st := range steps {
		_, err := f.j.Append(journal.Record{ID: tx, Status: st, FolderID: "lib", Op: op, Path: rel, Staging: stagingDir + "/" + tx})
		must(f.t, err)
	}
	stage := f.abs(stagingDir + "/" + tx)
	must(f.t, os.MkdirAll(stage, 0o700))
	return stage
}

// wantFinished checks that tx is closed, including in the journal on disk.
func (f *fixture) wantFinished(tx string) {
	f.t.Helper()
	if !f.j.Finished(tx) {
		f.t.Fatalf("%s is still open", tx)
	}
	reopened, err := journal.Open(filepath.Dir(f.j.Path()))
	must(f.t, err)
	if !reopened.Finished(tx) {
		f.t.Fatalf("%s is open again after reopening the journal", tx)
	}
}

func TestRecoverRollsBackAHalfSwappedPackage(t *testing.T) {
	f := newFixture(t)
	old := map[string]string{"Index.xml": "old", "Data/a.png": "a"}
	f.pkg("Doc.pages", old)
	stage := f.interrupted(txA, "set", "Doc.pages", journal.StatusStarted, journal.StatusPrepared)
	f.write(stagingDir+"/"+txA+"/new/Index.xml", "new")
	// The crash came after the old package was set aside, before the new one moved in.
	must(t, os.Rename(f.abs("Doc.pages"), filepath.Join(stage, "old")))

	must(t, f.env.Recover())
	if got := tree(t, f.abs("Doc.pages")); !maps.Equal(got, old) {
		t.Fatalf("got %v", got)
	}
	f.wantFinished(txA)
	f.wantNoStaging()
}

func TestRecoverKeepsACompletedSwap(t *testing.T) {
	f := newFixture(t)
	f.pkg("Doc.pages", map[string]string{"Index.xml": "old"})
	stage := f.interrupted(txA, "set", "Doc.pages", journal.StatusStarted, journal.StatusPrepared)
	must(t, os.Rename(f.abs("Doc.pages"), filepath.Join(stage, "old")))
	// The new package moved in; the crash came before the commit was journaled.
	f.write("Doc.pages/Index.xml", "new")

	must(t, f.env.Recover())
	if got := tree(t, f.abs("Doc.pages")); !maps.Equal(got, map[string]string{"Index.xml": "new"}) {
		t.Fatalf("got %v", got)
	}
	f.wantFinished(txA)
	f.wantNoStaging()
}

func TestRecoverDiscardsAStartedTransaction(t *testing.T) {
	f := newFixture(t)
	f.file("a.md", "current")
	f.interrupted(txA, "set", "a.md", journal.StatusStarted)
	f.write(stagingDir+"/"+txA+"/new", "half writ")

	must(t, f.env.Recover())
	if got := f.read("a.md"); got != "current" {
		t.Fatalf("got %q", got)
	}
	f.wantFinished(txA)
	f.wantNoStaging()
}

func TestRecoverPutsBackAnInterruptedDelete(t *testing.T) {
	f := newFixture(t)
	f.file("notes/a.md", "keep me")
	stage := f.interrupted(txA, "delete", "notes/a.md", journal.StatusStarted, journal.StatusPrepared)
	must(t, os.Rename(f.abs("notes/a.md"), filepath.Join(stage, "old")))

	must(t, f.env.Recover())
	if got := f.read("notes/a.md"); got != "keep me" {
		t.Fatalf("got %q", got)
	}
	f.wantFinished(txA)
	f.wantNoStaging()
}

func TestRecoverKeepsWhatAppearedAfterTheCrash(t *testing.T) {
	f := newFixture(t)
	f.pkg("Doc.pages", map[string]string{"Index.xml": "old"})
	stage := f.interrupted(txA, "set", "Doc.pages", journal.StatusStarted, journal.StatusPrepared)
	f.write(stagingDir+"/"+txA+"/new/Index.xml", "incoming")
	must(t, os.Rename(f.abs("Doc.pages"), filepath.Join(stage, "old")))
	f.write("Doc.pages/Index.xml", "saved after the crash")

	must(t, f.env.Recover())
	if got := f.read("Doc.pages/Index.xml"); got != "saved after the crash" {
		t.Fatalf("got %q", got)
	}
	f.wantFinished(txA)
}

func TestRecoverLeavesLegacyStagingAlone(t *testing.T) {
	f := newFixture(t)
	legacy := filepath.Join(t.TempDir(), "lib-0", "file")
	must(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	must(t, os.WriteFile(legacy, []byte("staged by an older version"), 0o600))
	for _, st := range []journal.Status{journal.StatusStarted, journal.StatusPrepared} {
		_, err := f.j.Append(journal.Record{ID: "lib-0", Status: st, FolderID: "lib", Op: "set", Path: "Doc.md", Staging: legacy})
		must(t, err)
	}

	must(t, f.env.Recover())
	if f.exists("Doc.md") {
		t.Fatal("moved an older version's staging into the library")
	}
	if b, err := os.ReadFile(legacy); err != nil || string(b) != "staged by an older version" {
		t.Fatalf("older staging changed: %q, %v", b, err)
	}
	f.wantFinished("lib-0")
}

func TestRecoverOnlyTouchesItsOwnLibrary(t *testing.T) {
	f := newFixture(t)
	f.file("Plan.md", "docs library file")
	notesRoot := t.TempDir()
	const txB = "tx-00000000000000b2"
	staged := filepath.Join(notesRoot, ".synk", "staging", txB, "new")
	must(t, os.MkdirAll(filepath.Dir(staged), 0o700))
	must(t, os.WriteFile(staged, []byte("notes library content"), 0o600))
	for _, st := range []journal.Status{journal.StatusStarted, journal.StatusPrepared} {
		_, err := f.j.Append(journal.Record{ID: txB, Status: st, FolderID: "notes", Op: "set", Path: "Plan.md", Staging: ".synk/staging/" + txB})
		must(t, err)
	}

	must(t, f.env.Recover())
	if got := f.read("Plan.md"); got != "docs library file" {
		t.Fatalf("recovering another library's write changed this one: %q", got)
	}
	if f.j.Finished(txB) {
		t.Fatal("closed another library's transaction")
	}

	notes, err := Open(notesRoot, "notes", f.store, f.vault, f.j)
	must(t, err)
	defer notes.Close()
	must(t, notes.Recover())
	if !f.j.Finished(txB) {
		t.Fatal("the notes library didn't recover its own transaction")
	}
	if _, err := os.Stat(filepath.Join(notesRoot, "Plan.md")); err == nil {
		t.Fatal("finished a write that never reached its commit point")
	}
}

func TestRecoverSweepsAbandonedStaging(t *testing.T) {
	f := newFixture(t)
	f.write(stagingDir+"/tx-00000000000000d4/new", "left by a finished transaction")
	f.write(stagingDir+"/stray/file", "unknown")
	must(t, f.env.Recover())
	f.wantNoStaging()
}
