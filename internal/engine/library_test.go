package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/apps"
	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

func TestUnreadableFolderDoesNotPropagateDeletes(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "Projects/plan.md", "plan")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "Projects/plan.md", "plan")

	sub := filepath.Join(dirA, "Projects")
	must(t, os.Chmod(sub, 0o000))
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	writeFile(t, dirB, "Projects/new.md", "new")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "Projects/plan.md", "plan")

	// Readable again, a real delete goes through, and what B added arrives.
	must(t, os.Chmod(sub, 0o700))
	must(t, os.Remove(filepath.Join(sub, "plan.md")))
	must(t, SyncOnce(a, b, "notes"))
	wantGone(t, dirB, "Projects/plan.md")
	wantFile(t, dirA, "Projects/new.md", "new")
}

func TestSymlinkedLibraryRootDoesNotPropagateDeletes(t *testing.T) {
	a := openNode(t, "A")
	b := openNode(t, "B")
	must(t, Pair(a, b))
	dirA := filepath.Join(t.TempDir(), "notes")
	target := filepath.Join(t.TempDir(), "real-notes")
	must(t, os.MkdirAll(target, 0o700))
	linkB := filepath.Join(t.TempDir(), "notes-link")
	if err := os.Symlink(target, linkB); err != nil {
		t.Skip(err)
	}
	must(t, a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirA}))
	must(t, b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: linkB}))
	writeFile(t, dirA, "a.md", "from A")

	must(t, SyncOnce(a, b, "notes"))
	must(t, SyncOnce(a, b, "notes"))

	wantFile(t, dirA, "a.md", "from A")
	wantFile(t, target, "a.md", "from A")
}

func TestICloudEvictionIsNotADelete(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "notes/todo.md", "body")
	must(t, SyncOnce(a, b, "notes"))

	// iCloud evicts notes/todo.md by replacing it with notes/.todo.md.icloud.
	must(t, os.Remove(filepath.Join(dirA, "notes", "todo.md")))
	writeFile(t, dirA, "notes/.todo.md.icloud", "bplist00")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "notes/todo.md", "body")
	wantGone(t, dirB, "notes/.todo.md.icloud")
	if got := a.Status().Libraries[0].Placeholders; !slices.Equal(got, []string{"notes/.todo.md.icloud"}) {
		t.Fatalf("placeholders %v", got)
	}

	// An edit on B isn't written over the placeholder; it arrives once
	// iCloud downloads the file again.
	writeFile(t, dirB, "notes/todo.md", "edited on B")
	must(t, SyncOnce(a, b, "notes"))
	wantGone(t, dirA, "notes/todo.md")
	must(t, os.Remove(filepath.Join(dirA, "notes", ".todo.md.icloud")))
	writeFile(t, dirA, "notes/todo.md", "body")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirA, "notes/todo.md", "edited on B")
}

func TestNewlyIgnoredFileIsNotDeletedOnPeers(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	const ws = ".obsidian/workspace.json"
	writeFile(t, dirA, ws, `{"left":"files"}`)
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, ws, `{"left":"files"}`)

	// A now runs a release whose preset ignores the file, as when a preset's
	// ignore list changes, or two devices run different releases.
	setPreset(t, a, "notes", "obsidian")
	writeFile(t, dirA, ws, `{"left":"search"}`)
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, ws, `{"left":"files"}`)

	// Synced again, A's edit is a newer version of what B has, not a
	// conflict with it.
	setPreset(t, a, "notes", "custom")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, ws, `{"left":"search"}`)
	if hasConflict(filepath.Join(dirB, ".obsidian")) {
		t.Fatal("un-ignoring an edited file made a conflict copy")
	}
}

func TestPeersDontWriteWhatThisDeviceIgnores(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	setPreset(t, a, "notes", "obsidian")
	writeFile(t, dirB, ".obsidian/workspace.json", "B's layout")
	writeFile(t, dirB, "a.md", "a")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirA, "a.md", "a")
	wantGone(t, dirA, ".obsidian/workspace.json")
}

func TestPeersCannotWriteIntoAppLibraries(t *testing.T) {
	_, b, _, dirB := pairedLibrary(t)
	payload := []byte("not a photo")
	hash := atomicfile.HashBytes(payload)
	remote := reconcile.NewIndex("notes", "PEER")
	for _, p := range []string{"Photos Library.photoslibrary", "Old.photoslibrary/database/Photos.sqlite"} {
		remote.Entries[p] = reconcile.Entry{Path: p, Kind: reconcile.KindFile, Hash: hash, Size: int64(len(payload)), Vector: vector.Vector{"PEER": 1}}
	}
	must(t, b.PullFrom(remote, func(string) ([]byte, error) {
		t.Error("fetched content for an app library")
		return payload, nil
	}))
	wantGone(t, dirB, "Photos Library.photoslibrary")
	wantGone(t, dirB, "Old.photoslibrary")
}

func TestMassDeleteIsHeldUntilConfirmed(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeNotes(t, dirA, 0, 60)
	must(t, SyncOnce(a, b, "notes"))

	removeNotes(t, dirA, 0, 3)
	must(t, SyncOnce(a, b, "notes"))
	wantGone(t, dirB, note(0))

	removeNotes(t, dirA, 3, 58)
	must(t, SyncOnce(a, b, "notes"))
	for i := 3; i < 60; i++ {
		wantFile(t, dirB, note(i), body(i))
	}
	held := a.HeldDeletes("notes")
	if len(held) != 55 || !slices.Contains(held, note(3)) {
		t.Fatalf("held %d deletes: %v", len(held), held)
	}
	if got := a.Status().Libraries[0].HeldDeletes; len(got) != 55 {
		t.Fatalf("status shows %d held deletes", len(got))
	}

	if _, err := a.ConfirmDeletes("notes", held[1:]); !errors.Is(err, ErrHeldChanged) {
		t.Fatalf("confirming a different list: %v", err)
	}
	confirmed, err := a.ConfirmDeletes("notes", held)
	must(t, err)
	if len(confirmed) != 55 {
		t.Fatalf("confirmed %d", len(confirmed))
	}
	must(t, SyncOnce(a, b, "notes"))
	for i := 3; i < 60; i++ {
		if i < 58 {
			wantGone(t, dirB, note(i))
		} else {
			wantFile(t, dirB, note(i), body(i))
		}
	}
	if held := a.HeldDeletes("notes"); len(held) > 0 {
		t.Fatalf("still held: %v", held)
	}
}

func TestMovingAFolderIsNotHeld(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeNotes(t, filepath.Join(dirA, "Inbox"), 0, 60)
	must(t, SyncOnce(a, b, "notes"))

	must(t, os.Rename(filepath.Join(dirA, "Inbox"), filepath.Join(dirA, "Archive")))
	must(t, SyncOnce(a, b, "notes"))

	if held := a.HeldDeletes("notes"); len(held) > 0 {
		t.Fatalf("a move was held as %d deletes", len(held))
	}
	wantFile(t, dirB, "Archive/"+note(7), body(7))
	wantGone(t, dirB, "Inbox")
}

func TestRestoreDeletesPutsFilesBack(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeNotes(t, dirA, 0, 20)
	must(t, SyncOnce(a, b, "notes"))
	removeNotes(t, dirA, 0, 15)
	_, err := a.ScanLibrary("notes")
	must(t, err)
	if held := a.HeldDeletes("notes"); len(held) != 15 {
		t.Fatalf("held %v", held)
	}

	// One file's content is gone from this device; it comes back from B.
	lost := a.Index("notes").Entries[note(0)].Hash
	must(t, os.Remove(filepath.Join(a.Store.Root, "objects", lost[:2], lost[2:4], lost)))
	restored, err := a.RestoreDeletes("notes")
	if err == nil {
		t.Fatal("restoring content this device doesn't have reported no error")
	}
	if len(restored) != 15 {
		t.Fatalf("restored %v", restored)
	}
	for i := 1; i < 20; i++ {
		wantFile(t, dirA, note(i), body(i))
	}
	must(t, SyncOnce(a, b, "notes"))
	for i := range 20 {
		wantFile(t, dirA, note(i), body(i))
		wantFile(t, dirB, note(i), body(i))
	}
	if held := a.HeldDeletes("notes"); len(held) > 0 {
		t.Fatalf("still held: %v", held)
	}
}

func TestUnmountedLibraryIsNotScanned(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "a.md", "a")
	must(t, SyncOnce(a, b, "notes"))

	// The drive is unmounted, leaving an empty mount point.
	must(t, os.Rename(dirA, dirA+".away"))
	must(t, os.Mkdir(dirA, 0o700))
	if _, err := a.ScanLibrary("notes"); !errors.Is(err, ErrLibraryMissing) {
		t.Fatalf("scanning an empty mount point: %v", err)
	}
	if err := SyncOnce(a, b, "notes"); err == nil {
		t.Fatal("synced a library whose folder is missing")
	}
	wantFile(t, dirB, "a.md", "a")
	if msg := a.Status().Libraries[0].Error; !strings.Contains(msg, "is the drive mounted") {
		t.Fatalf("status doesn't say why the library isn't syncing: %q", msg)
	}

	must(t, os.Remove(dirA))
	must(t, os.Rename(dirA+".away", dirA))
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "a.md", "a")
	if msg := a.Status().Libraries[0].Error; msg != "" {
		t.Fatalf("a fixed library still shows an error: %q", msg)
	}
}

func TestLibrariesWithoutAMarkerGetOneIfTheyLookRight(t *testing.T) {
	a, _, dirA, _ := pairedLibrary(t)
	writeFile(t, dirA, "a.md", "a")
	_, err := a.ScanLibrary("notes")
	must(t, err)

	// Libraries enabled by older releases have no .synk folder.
	must(t, os.RemoveAll(filepath.Join(dirA, ".synk")))
	_, err = a.ScanLibrary("notes")
	must(t, err)
	if st, err := os.Stat(filepath.Join(dirA, ".synk")); err != nil || !st.IsDir() {
		t.Fatalf("marker: %v", err)
	}
}

func TestEnableRefusesUnsafeFolders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	n := openNodeAt(t, filepath.Join(home, "Library", "Application Support", "Synk"), "A")
	notes := filepath.Join(home, "Writing", "Notes")
	must(t, n.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: notes}))
	icloud := filepath.Join(home, "Library", "Mobile Documents", "iCloud~md~obsidian", "Documents")
	must(t, os.MkdirAll(icloud, 0o700))
	link := filepath.Join(home, "Vault")
	if err := os.Symlink(icloud, link); err != nil {
		t.Skip(err)
	}

	for name, p := range map[string]string{
		"iCloud":                icloud,
		"a symlink into iCloud": link,
		"cloud storage":         filepath.Join(home, "Library", "CloudStorage", "Dropbox", "Notes"),
		"home":                  home,
		"above home":            filepath.Dir(home),
		"the disk":              "/",
		"Synk's data":           filepath.Join(n.DataDir, "notes"),
		"above Synk's data":     filepath.Join(home, "Library", "Application Support"),
		"inside a library":      filepath.Join(notes, "sub"),
		"above a library":       filepath.Join(home, "Writing"),
		"an app library":        filepath.Join(home, "Pictures", "Photos Library.photoslibrary"),
		"a relative path":       "notes",
	} {
		if err := n.EnableLibrary(config.Library{ID: "other", Preset: "custom", Label: "o", Path: p}); !errors.Is(err, ErrLibraryPath) {
			t.Errorf("%s (%s): %v", name, p, err)
		}
	}
	if n.Config.LibraryByID("other") != nil {
		t.Fatal("a refused library was saved")
	}
	must(t, n.EnableLibrary(config.Library{ID: "notes", Preset: "obsidian", Label: "n", Path: notes}))
}

func TestEnableValidatesIDsAndPresets(t *testing.T) {
	n := openNode(t, "A")
	dir := filepath.Join(t.TempDir(), "notes")
	for _, id := range []string{"", "../x", "a/b", `a\b`, "Notes", "-x", ".synk", "a b", "é", strings.Repeat("a", 65)} {
		if err := n.EnableLibrary(config.Library{ID: id, Preset: "custom", Path: dir}); !errors.Is(err, ErrInvalidLibrary) {
			t.Errorf("ID %q: %v", id, err)
		}
	}
	if err := n.EnableLibrary(config.Library{ID: "notes", Preset: "nope", Path: dir}); !errors.Is(err, ErrInvalidLibrary) {
		t.Errorf("unknown preset: %v", err)
	}
	if len(n.Libraries()) != 0 {
		t.Fatalf("saved %+v", n.Libraries())
	}
	for _, p := range apps.Catalog() {
		if !ValidLibraryID(p.ID) {
			t.Errorf("preset %q isn't a valid library ID, but the app enables libraries with the preset's ID", p.ID)
		}
	}

	id := strings.Repeat("a", 64)
	must(t, n.EnableLibrary(config.Library{ID: id, Preset: "custom", Path: dir}))
	if lib, ok := n.Library(id); !ok || lib.Label != "Any folder" {
		t.Fatalf("an empty label becomes the preset's name: %+v", lib)
	}
	if err := n.EnableLibrary(config.Library{ID: id, Preset: "custom", Path: filepath.Join(t.TempDir(), "elsewhere")}); !errors.Is(err, ErrLibraryExists) {
		t.Fatalf("moved a library by enabling its ID at another folder: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	must(t, n.EnableLibrary(config.Library{ID: id, Preset: "obsidian", Path: link}))
}

func TestStatusSharesNothingWithConfig(t *testing.T) {
	a, b := openNode(t, "A"), openNode(t, "B")
	must(t, Pair(a, b))
	a.Config.Peers[0].Addresses = []string{"192.0.2.1:7420"}
	must(t, a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes"), ShareWith: []string{b.Device.DeviceID}}))

	st := a.Status()
	st.Peers[0].Addresses[0] = "changed"
	st.Libraries[0].ShareWith[0] = "changed"
	if a.Config.Peers[0].Addresses[0] == "changed" || a.Config.Libraries[0].ShareWith[0] == "changed" {
		t.Fatal("Status shares slices with the config, which callers encode after the lock is released")
	}
	p, _ := a.Peer(b.Device.DeviceID)
	p.Addresses[0] = "changed"
	lib, _ := a.Library("notes")
	lib.ShareWith[0] = "changed"
	if a.Config.Peers[0].Addresses[0] == "changed" || a.Config.Libraries[0].ShareWith[0] == "changed" {
		t.Fatal("Peer or Library shares slices with the config")
	}
}

func setPreset(t *testing.T, n *Node, id, preset string) {
	t.Helper()
	lib := *n.Config.LibraryByID(id)
	lib.Preset = preset
	must(t, n.EnableLibrary(lib))
}

func note(i int) string { return fmt.Sprintf("n%02d.md", i) }
func body(i int) string { return fmt.Sprintf("note %d", i) }

func writeNotes(t *testing.T, dir string, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		writeFile(t, dir, note(i), body(i))
	}
}

func removeNotes(t *testing.T, dir string, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		must(t, os.Remove(filepath.Join(dir, note(i))))
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	must(t, os.WriteFile(p, []byte(content), 0o600))
}

func wantFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil || string(got) != content {
		t.Fatalf("%s: %q, %v; want %q", rel, got, err, content)
	}
}

func wantGone(t *testing.T, dir, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists (%v)", rel, err)
	}
}
