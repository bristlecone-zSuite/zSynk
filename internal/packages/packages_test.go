package packages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/blob"
)

func TestPackageManifestStable(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "Talk.key")
	if err := os.MkdirAll(filepath.Join(pkg, "Data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Index.zip"), []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Data", "img.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, raw1, err := BuildManifest(pkg, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, raw2, err := BuildManifest(pkg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if HashManifest(raw1) != HashManifest(raw2) {
		t.Fatal("unstable hash")
	}
	if !IsPackageName("Talk.key") || IsPackageName("Talk.md") {
		t.Fatal("ext detect")
	}
}

func TestMaterialize(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "Essay.textbundle")
	if err := os.MkdirAll(filepath.Join(pkg, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "text.md"), []byte("# hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "info.json"), []byte(`{"type":"note"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	man, _, err := BuildManifest(pkg, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blob.New(filepath.Join(dir, "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range man.Files {
		data, err := os.ReadFile(filepath.Join(pkg, filepath.FromSlash(f.RelPath)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(data); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "out.textbundle")
	if err := Materialize(openEmptyRoot(t, out), man, store.Open); err != nil {
		t.Fatal(err)
	}
	got, _, err := BuildManifest(out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if HashManifest(mustCanon(t, man)) != HashManifest(mustCanon(t, got)) {
		t.Fatal("round trip mismatch")
	}
}

func TestMaterializeRejectsUnsafeManifests(t *testing.T) {
	dir := t.TempDir()
	store, err := blob.New(filepath.Join(dir, "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("planted")
	h, err := store.Put(payload)
	if err != nil {
		t.Fatal(err)
	}
	part := func(rel string) FilePart { return FilePart{RelPath: rel, Hash: h, Size: int64(len(payload))} }
	cases := map[string]*Manifest{
		"parent":     {Kind: KindPackage, Files: []FilePart{part("../planted")}},
		"absolute":   {Kind: KindPackage, Files: []FilePart{part(filepath.Join(dir, "planted"))}},
		"synk":       {Kind: KindPackage, Files: []FilePart{part(".synk/planted")}},
		"duplicate":  {Kind: KindPackage, Files: []FilePart{part("a"), part("a")}},
		"unsorted":   {Kind: KindPackage, Files: []FilePart{part("b"), part("a")}},
		"bad hash":   {Kind: KindPackage, Files: []FilePart{{RelPath: "a", Hash: "../../x", Size: 1}}},
		"wrong kind": {Kind: KindFile, Files: []FilePart{part("a")}},
	}
	for name, m := range cases {
		out := filepath.Join(dir, "pkgs", name, "out.textbundle")
		if err := Materialize(openEmptyRoot(t, out), m, store.Open); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, p := range []string{filepath.Join(dir, "planted"), filepath.Join(dir, "pkgs", "planted")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("a manifest part was written outside the package at %s", p)
		}
	}
}

func TestMaterializeRejectsWrongSize(t *testing.T) {
	dir := t.TempDir()
	store, err := blob.New(filepath.Join(dir, "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := store.Put([]byte("four"))
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{Kind: KindPackage, Files: []FilePart{{RelPath: "a", Hash: h, Size: 3}}}
	if err := Materialize(openEmptyRoot(t, filepath.Join(dir, "out")), m, store.Open); err == nil {
		t.Fatal("a part whose size disagrees with its blob was accepted")
	}
}

func TestBuildManifestSkipsSpecialFiles(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "Doc.pages")
	if err := os.MkdirAll(pkg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Index.zip"), []byte("i"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(pkg, "link")); err != nil {
		t.Fatal(err)
	}
	m, _, err := BuildManifest(pkg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].RelPath != "Index.zip" {
		t.Fatalf("%+v", m.Files)
	}
}

func openEmptyRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func mustCanon(t *testing.T, m *Manifest) []byte {
	t.Helper()
	b, err := Canonical(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
