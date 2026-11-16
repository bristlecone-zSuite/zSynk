package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
)

// Pausing a library stops it syncing, and resuming starts it again.
func TestPauseStopsSyncingUntilResume(t *testing.T) {
	a, b := openNode(t, "A"), openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	dirB := library(t, b, "notes")
	writeFile(t, dirA, "a.md", "hello")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "a.md", "hello")

	// A paused library refuses a peer's session.
	must(t, b.PauseLibrary("notes"))
	if err := a.SyncWith(listen(t, b), "notes"); err == nil {
		t.Fatal("a sync with a paused library succeeded")
	}
	writeFile(t, dirA, "a.md", "changed while paused")
	if err := a.SyncWith(listen(t, b), "notes"); err == nil {
		t.Fatal("a paused library synced")
	}
	wantFile(t, dirB, "a.md", "hello")
	for _, l := range b.Status().Libraries {
		if l.ID == "notes" && !l.Paused {
			t.Fatal("status doesn't show the library as paused")
		}
	}

	// Resuming syncs everything that waited.
	must(t, b.ResumeLibrary("notes"))
	must(t, a.SyncWith(listen(t, b), "notes"))
	wantFile(t, dirB, "a.md", "changed while paused")
}

// Pausing a library that doesn't exist is an error, and pausing or
// resuming twice is not.
func TestPauseUnknownLibrary(t *testing.T) {
	n := openNode(t, "A")
	if err := n.PauseLibrary("nope"); err == nil {
		t.Fatal("paused a library that doesn't exist")
	}
	if err := n.ResumeLibrary("nope"); err == nil {
		t.Fatal("resumed a library that doesn't exist")
	}
	library(t, n, "notes")
	must(t, n.PauseLibrary("notes"))
	must(t, n.PauseLibrary("notes"))
	must(t, n.ResumeLibrary("notes"))
	must(t, n.ResumeLibrary("notes"))
}

// Removing a library stops syncing it and keeps everything: the files,
// their .synk marker, the versions in the vault, and the index, so
// enabling it again picks up where it left off.
func TestRemoveLibraryKeepsEverything(t *testing.T) {
	a, b := openNode(t, "A"), openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	dirB := library(t, b, "notes")
	writeFile(t, dirA, "a.md", "hello")
	writeFile(t, dirA, "sub/b.md", "nested")
	must(t, SyncOnce(a, b, "notes"))

	// A replaced version leaves a copy in B's vault.
	writeFile(t, dirA, "a.md", "goodbye")
	must(t, SyncOnce(a, b, "notes"))
	vs, err := b.Versions("notes", "a.md")
	must(t, err)
	if len(vs) == 0 {
		t.Fatal("the vault kept no version of the replaced file")
	}

	must(t, b.RemoveLibrary("notes"))
	for _, l := range b.Config.Libraries {
		if l.ID == "notes" {
			t.Fatal("the removed library is still configured")
		}
	}
	if got := b.Status(); len(got.Libraries) != 0 {
		t.Fatalf("status lists %d libraries after removing the only one", len(got.Libraries))
	}
	for _, p := range []string{filepath.Join(dirB, "a.md"), filepath.Join(dirB, "sub", "b.md"), filepath.Join(dirB, ".synk")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("removing the library touched %s: %v", p, err)
		}
	}
	vs2, err := b.Vault.Versions("notes", "a.md")
	must(t, err)
	if len(vs2) != len(vs) {
		t.Fatalf("removing the library changed the versions it keeps: %d to %d", len(vs), len(vs2))
	}
	if _, err := os.Stat(b.indexPath("notes")); err != nil {
		t.Fatal("the index a re-enabled library would resume from is gone")
	}

	// The peer can't sync it here any more, either way.
	if err := a.SyncWith(listen(t, b), "notes"); err == nil {
		t.Fatal("a sync with a removed library succeeded")
	}

	// Enabling the same folder again resumes where the index left off.
	must(t, b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dirB}))
	if _, err := b.ScanLibrary("notes"); err != nil {
		t.Fatal(err)
	}
	if got := b.Status().Libraries[0].Files; got != 2 {
		t.Fatalf("the re-enabled library sees %d files; want the 2 it had", got)
	}
}

// Unpairing removes the device and everything that trusted it: the
// libraries stop sharing with it, and its connections are refused.
func TestUnpairStopsEverySyncWithThatDevice(t *testing.T) {
	a, b := openNode(t, "A"), openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	dirB := library(t, b, "notes")
	writeFile(t, dirA, "a.md", "hello")
	must(t, SyncOnce(a, b, "notes"))

	// An explicit share list must lose the unpaired device too.
	must(t, a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dirA, ShareWith: []string{b.Device.DeviceID}}))

	must(t, a.Unpair(b.Device.DeviceID))
	if _, ok := a.Peer(b.Device.DeviceID); ok {
		t.Fatal("the unpaired device is still configured")
	}
	for _, l := range a.Config.Libraries {
		for _, id := range l.ShareWith {
			if id == b.Device.DeviceID {
				t.Fatal("a library still shares with the unpaired device")
			}
		}
	}
	if err := a.SyncWith(listen(t, b), "notes"); err == nil {
		t.Fatal("a sync with an unpaired device succeeded")
	}
	// And the other direction: the device we unpaired can't sync here.
	if err := b.SyncWith(listen(t, a), "notes"); err == nil {
		t.Fatal("the unpaired device synced with us")
	}
	if _, err := os.Stat(filepath.Join(dirB, "a.md")); err != nil {
		t.Fatal("unpairing touched the files that had synced")
	}
	if _, err := os.Stat(filepath.Join(dirA, "a.md")); err != nil {
		t.Fatal("unpairing touched this device's own files")
	}
}

// Unpairing a device that isn't paired is an error.
func TestUnpairUnknownDevice(t *testing.T) {
	n := openNode(t, "A")
	if err := n.Unpair("QQKX-AHEH-BZYC-WDA9"); err == nil {
		t.Fatal("unpaired a device that was never paired")
	}
}
