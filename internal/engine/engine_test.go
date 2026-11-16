package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
)

func TestTwoDeviceMarkdownAndPackage(t *testing.T) {
	a := openNode(t, "Mac")
	b := openNode(t, "iPhone")
	if err := Pair(a, b); err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(t.TempDir(), "notes")
	dirB := filepath.Join(t.TempDir(), "notes")
	lib := config.Library{ID: "notes", Preset: "obsidian", Label: "Notes", Path: dirA}
	libB := lib
	libB.Path = dirB
	if err := a.EnableLibrary(lib); err != nil {
		t.Fatal(err)
	}
	if err := b.EnableLibrary(libB); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dirA, "Inbox.md"), []byte("# hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(dirA, "Talk.key")
	if err := os.MkdirAll(filepath.Join(pkg, "Data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Index.zip"), []byte("index-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Data", "slide.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SyncOnce(a, b, "notes"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "Inbox.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# hello" {
		t.Fatal(string(got))
	}
	if _, err := os.Stat(filepath.Join(dirB, "Talk.key", "Index.zip")); err != nil {
		t.Fatal("package did not arrive intact")
	}

	// Concurrent edits of the same note.
	if err := os.WriteFile(filepath.Join(dirA, "Inbox.md"), []byte("# from A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "Inbox.md"), []byte("# from B"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncOnce(a, b, "notes"); err != nil {
		t.Fatal(err)
	}
	aBytes, _ := os.ReadFile(filepath.Join(dirA, "Inbox.md"))
	bBytes, _ := os.ReadFile(filepath.Join(dirB, "Inbox.md"))
	if string(aBytes) != string(bBytes) {
		t.Fatalf("original path should converge, A=%q B=%q", aBytes, bBytes)
	}
	if !hasConflict(dirA) || !hasConflict(dirB) {
		t.Fatal("expected conflict copies on both devices")
	}
	// Neither version lost.
	allA := catAll(t, dirA)
	if !strings.Contains(allA, "# from A") || !strings.Contains(allA, "# from B") {
		t.Fatalf("lost an edit: %s", allA)
	}
}

func TestDeleteDoesNotWinConcurrentEdit(t *testing.T) {
	a := openNode(t, "A")
	b := openNode(t, "B")
	_ = Pair(a, b)
	dirA := filepath.Join(t.TempDir(), "n")
	dirB := filepath.Join(t.TempDir(), "n")
	_ = a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirA})
	_ = b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirB})
	_ = os.WriteFile(filepath.Join(dirA, "keep.md"), []byte("v1"), 0o600)
	if err := SyncOnce(a, b, "notes"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "keep.md"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dirB, "keep.md")); err != nil {
		t.Fatal(err)
	}
	if err := SyncOnce(a, b, "notes"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "keep.md"))
	if err != nil {
		t.Fatal("edit must survive concurrent delete")
	}
	if string(got) != "edited" {
		t.Fatalf("%q", got)
	}
}

func TestCorruptBlobQuarantined(t *testing.T) {
	a := openNode(t, "A")
	b := openNode(t, "B")
	_ = Pair(a, b)
	dirA := filepath.Join(t.TempDir(), "n")
	dirB := filepath.Join(t.TempDir(), "n")
	_ = a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirA})
	_ = b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirB})
	_ = os.WriteFile(filepath.Join(dirA, "x.md"), []byte("real"), 0o600)
	ia, err := a.ExportIndex("notes")
	if err != nil {
		t.Fatal(err)
	}
	evil := func(hash string) ([]byte, error) {
		return []byte("not-the-bytes"), nil
	}
	if err := b.PullFrom(ia, evil); err == nil {
		t.Fatal("expected hash mismatch")
	}
	if _, err := os.Stat(filepath.Join(dirB, "x.md")); err == nil {
		t.Fatal("must not write unauthenticated bytes")
	}
}

func openNode(t testing.TB, name string) *Node {
	t.Helper()
	return openNodeAt(t, t.TempDir(), name)
}

func openNodeAt(t testing.TB, dir, name string) *Node {
	t.Helper()
	n, err := Open(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	n.PackageSettle = 0
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func hasConflict(dir string) bool {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".synk-conflict-") {
			return true
		}
	}
	return false
}

func catAll(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			raw, _ := os.ReadFile(path)
			b.Write(raw)
			b.WriteByte('\n')
		}
		return nil
	})
	return b.String()
}
