package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

// A file a sync replaced can be put back from the vault. Putting it back
// keeps what it replaces, and paired devices get it as an edit made here.
func TestRestoreAReplacedFile(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "a.md", "mine")
	must(t, SyncOnce(a, b, "notes"))
	writeFile(t, dirB, "a.md", "theirs")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirA, "a.md", "theirs")

	vs, err := a.Versions("notes", "a.md")
	must(t, err)
	if len(vs) != 1 || vs[0].Reason != "pre-overwrite" || vs[0].Size != int64(len("mine")) {
		t.Fatalf("versions of a.md: %+v", vs)
	}
	got, err := a.RestoreVersion("notes", "a.md", "")
	must(t, err)
	if got != vs[0] {
		t.Fatalf("restored %+v; want the newest, %+v", got, vs[0])
	}
	wantFile(t, dirA, "a.md", "mine")
	if vs, err := a.Versions("notes", "a.md"); err != nil || len(vs) != 2 || vs[0].Size != int64(len("theirs")) {
		t.Fatalf("after the restore, a.md's versions are %+v, %v", vs, err)
	}
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "a.md", "mine")
	if hasConflict(dirA) || hasConflict(dirB) {
		t.Fatal("a restore made a conflict")
	}
}

// Any version can be restored, not just the newest, which is the one
// restored when none is named.
func TestRestoreAnOlderVersion(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	for _, s := range []string{"one", "two", "three"} {
		writeFile(t, dirB, "a.md", s)
		must(t, SyncOnce(a, b, "notes"))
	}
	vs, err := a.Versions("notes", "a.md")
	must(t, err)
	if len(vs) != 2 {
		t.Fatalf("versions of a.md: %+v", vs)
	}
	_, err = a.RestoreVersion("notes", "a.md", "")
	must(t, err)
	wantFile(t, dirA, "a.md", "two")
	_, err = a.RestoreVersion("notes", "a.md", vs[1].ID)
	must(t, err)
	wantFile(t, dirA, "a.md", "one")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "a.md", "one")
}

func TestRestoreADeletedFile(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "notes/plan.md", "plan")
	must(t, SyncOnce(a, b, "notes"))
	must(t, os.Remove(filepath.Join(dirB, "notes", "plan.md")))
	must(t, SyncOnce(a, b, "notes"))
	wantGone(t, dirA, "notes/plan.md")

	all, err := a.Versions("notes", "")
	must(t, err)
	if len(all) != 1 || all[0].Path != "notes/plan.md" || all[0].Reason != "pre-delete" {
		t.Fatalf("the library's versions: %+v", all)
	}
	_, err = a.RestoreVersion("notes", "notes/plan.md", all[0].ID)
	must(t, err)
	wantFile(t, dirA, "notes/plan.md", "plan")
	if st := a.Status(); st.Libraries[0].Files != 1 {
		t.Fatalf("after the restore, the status counts %d files", st.Libraries[0].Files)
	}
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "notes/plan.md", "plan")
}

// A package's version holds what the package did, Finder's files too, and
// what Synk ignores doesn't count against its copy.
func TestRestoreAPackage(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "Talk.key/Index.zip", "v1")
	writeFile(t, dirA, "Talk.key/Data/a.png", "png")
	writeFile(t, dirA, "Talk.key/.DS_Store", "finder")
	must(t, SyncOnce(a, b, "notes"))
	writeFile(t, dirB, "Talk.key/Index.zip", "v2")
	must(t, os.Remove(filepath.Join(dirB, "Talk.key", "Data", "a.png")))
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirA, "Talk.key/Index.zip", "v2")
	wantGone(t, dirA, "Talk.key/Data/a.png")

	ver, err := a.RestoreVersion("notes", "Talk.key", "")
	must(t, err)
	if ver.Kind != "package" {
		t.Fatalf("restored %+v", ver)
	}
	wantFile(t, dirA, "Talk.key/Index.zip", "v1")
	wantFile(t, dirA, "Talk.key/Data/a.png", "png")
	must(t, SyncOnce(a, b, "notes"))
	wantFile(t, dirB, "Talk.key/Index.zip", "v1")
	wantFile(t, dirB, "Talk.key/Data/a.png", "png")
}

// A version whose copy changed since it was kept isn't restored.
func TestDamagedVersionsAreNotRestored(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	writeFile(t, dirA, "a.md", "mine")
	must(t, SyncOnce(a, b, "notes"))
	writeFile(t, dirB, "a.md", "theirs")
	must(t, SyncOnce(a, b, "notes"))
	vs, err := a.Versions("notes", "a.md")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(a.Vault.PathDir("notes", "a.md"), vs[0].ID, "content"), []byte("rot!"), 0o600))
	if _, err := a.RestoreVersion("notes", "a.md", vs[0].ID); !errors.Is(err, ErrDamagedVersion) {
		t.Fatalf("restoring a damaged version: %v", err)
	}
	wantFile(t, dirA, "a.md", "theirs")
}

func TestVersionErrors(t *testing.T) {
	n := openNode(t, "Mac")
	writeFile(t, library(t, n, "notes"), "a.md", "only")
	for _, c := range []struct {
		lib, rel, version string
		want              error
	}{
		{"other", "a.md", "", ErrUnknownLibrary},
		{"notes", "../a.md", "", ErrInvalidPath},
		{"notes", "", "", ErrInvalidPath},
		{"notes", "a.md", "", vault.ErrNoVersion},
		{"notes", "a.md", "../../a.md", vault.ErrNoVersion},
	} {
		if _, err := n.RestoreVersion(c.lib, c.rel, c.version); !errors.Is(err, c.want) {
			t.Errorf("RestoreVersion(%q, %q, %q): %v; want %v", c.lib, c.rel, c.version, err, c.want)
		}
	}
	if _, err := n.Versions("other", ""); !errors.Is(err, ErrUnknownLibrary) {
		t.Errorf("Versions of an unknown library: %v", err)
	}
	if _, err := n.Versions("notes", "/etc/passwd"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Versions of an absolute path: %v", err)
	}
	if vs, err := n.Versions("notes", ""); err != nil || len(vs) != 0 || vs == nil {
		t.Errorf("Versions of a library with none: %#v, %v", vs, err)
	}
}
