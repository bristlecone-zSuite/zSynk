package apply

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/ignore"
	"github.com/bristlecone-zSuite/zSynk/internal/journal"
	"github.com/bristlecone-zSuite/zSynk/internal/packages"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

type fixture struct {
	t     *testing.T
	root  string
	store *blob.Store
	vault *vault.Vault
	j     *journal.Log
	env   *Env
	idx   *reconcile.Index
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir(), idx: reconcile.NewIndex("lib", "A")}
	must(t, os.Mkdir(filepath.Join(f.root, ".synk"), 0o700))
	var err error
	f.store, err = blob.New(t.TempDir())
	must(t, err)
	f.vault, err = vault.Open(t.TempDir())
	must(t, err)
	f.j, err = journal.Open(t.TempDir())
	must(t, err)
	f.env, err = Open(f.root, "lib", f.store, f.vault, f.j)
	must(t, err)
	t.Cleanup(func() { _ = f.env.Close() })
	return f
}

// content is what a set op points at.
type content struct {
	kind  reconcile.Kind
	hash  string
	size  int64
	parts []string
}

func (f *fixture) blob(data string) content {
	f.t.Helper()
	h, err := f.store.Put([]byte(data))
	must(f.t, err)
	return content{kind: reconcile.KindFile, hash: h, size: int64(len(data))}
}

// manifest stores a package's parts and manifest.
func (f *fixture) manifest(files map[string]string) content {
	f.t.Helper()
	m := &packages.Manifest{Kind: packages.KindPackage}
	for name, data := range files {
		c := f.blob(data)
		m.Files = append(m.Files, packages.FilePart{RelPath: name, Hash: c.hash, Size: c.size})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].RelPath < m.Files[j].RelPath })
	raw, err := packages.Canonical(m)
	must(f.t, err)
	h, err := f.store.Put(raw)
	must(f.t, err)
	c := content{kind: reconcile.KindPackage, hash: h}
	for _, p := range m.Files {
		c.size += p.Size
		c.parts = append(c.parts, p.Hash)
	}
	return c
}

// file writes a library file and records it in the index, as a scan would.
func (f *fixture) file(rel, data string) content {
	f.t.Helper()
	f.write(rel, data)
	c := f.blob(data)
	f.scanned(rel, c)
	return c
}

// pkg writes a package into the library and records it, as a scan would.
func (f *fixture) pkg(rel string, files map[string]string) content {
	f.t.Helper()
	for name, data := range files {
		f.write(rel+"/"+name, data)
	}
	c := f.manifest(files)
	_, raw, err := packages.BuildManifest(f.abs(rel), ignore.Compile(ignore.GlobalPatterns()))
	must(f.t, err)
	if packages.HashManifest(raw) != c.hash {
		f.t.Fatal("fixture manifest differs from the scanner's")
	}
	f.scanned(rel, c)
	return c
}

func (f *fixture) scanned(rel string, c content) {
	f.idx.Entries[rel] = reconcile.Entry{Path: rel, Kind: c.kind, Hash: c.hash, Size: c.size, Parts: c.parts, Vector: vector.Vector{"A": 1}}
}

func (f *fixture) abs(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

// write changes the library without recording it, like an edit after a scan.
func (f *fixture) write(rel, data string) {
	f.t.Helper()
	must(f.t, os.MkdirAll(filepath.Dir(f.abs(rel)), 0o700))
	must(f.t, os.WriteFile(f.abs(rel), []byte(data), 0o600))
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.abs(rel))
	if err != nil {
		f.t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(f.abs(rel))
	return err == nil
}

// tree returns every regular file under dir with its content.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	must(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	}))
	return out
}

func set(rel string, c content) reconcile.Op {
	return reconcile.Op{Kind: reconcile.OpSet, Path: rel, KindFile: c.kind, Hash: c.hash, Size: c.size, Parts: c.parts, Vector: vector.Vector{"A": 1, "B": 1}}
}

func del(rel string) reconcile.Op {
	return reconcile.Op{Kind: reconcile.OpDelete, Path: rel, KindFile: reconcile.KindFile, Deleted: true, Vector: vector.Vector{"A": 1, "B": 1}}
}

// apply runs ops and fails the test unless all of them applied.
func (f *fixture) apply(ops ...reconcile.Op) {
	f.t.Helper()
	deferred, err := f.env.Apply(ops, f.idx)
	if err != nil || len(deferred) > 0 {
		f.t.Fatalf("apply: deferred %v, err %v", deferred, err)
	}
	f.wantNoStaging()
}

func (f *fixture) wantNoStaging() {
	f.t.Helper()
	ents, err := os.ReadDir(filepath.Join(f.root, ".synk", "staging"))
	must(f.t, err)
	if len(ents) > 0 {
		f.t.Fatalf("staging left behind: %v", ents)
	}
}

// snapshot returns the content directory of the newest vault version of rel.
func (f *fixture) snapshot(rel string) string {
	f.t.Helper()
	dir := f.vault.PathDir("lib", rel)
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) == 0 {
		f.t.Fatalf("no vault snapshot of %s: %v", rel, err)
	}
	return filepath.Join(dir, ents[len(ents)-1].Name(), "content")
}

func (f *fixture) wantSnapshot(rel, data string) {
	f.t.Helper()
	b, err := os.ReadFile(f.snapshot(rel))
	must(f.t, err)
	if string(b) != data {
		f.t.Fatalf("vault snapshot of %s holds %q, want %q", rel, b, data)
	}
}

func TestApplySetAndDelete(t *testing.T) {
	f := newFixture(t)
	f.apply(set("notes/a.md", f.blob("hello")))
	if got := f.read("notes/a.md"); got != "hello" {
		t.Fatalf("got %q", got)
	}
	f.apply(set("notes/a.md", f.blob("hello again")))
	if got := f.read("notes/a.md"); got != "hello again" {
		t.Fatalf("got %q", got)
	}
	f.wantSnapshot("notes/a.md", "hello")

	f.apply(del("notes/a.md"))
	if f.exists("notes/a.md") {
		t.Fatal("not deleted")
	}
	if f.exists("notes") {
		t.Fatal("the folder the delete emptied was left behind")
	}
	if e := f.idx.Entries["notes/a.md"]; !e.Deleted || e.Hash == "" {
		t.Fatalf("tombstone %+v", e)
	}
	f.wantSnapshot("notes/a.md", "hello again")
}

func TestApplyConflictKeepsBoth(t *testing.T) {
	f := newFixture(t)
	mine := f.file("Essay.md", "aaa")
	theirs := f.blob("bbb")
	op := reconcile.Op{
		Kind: reconcile.OpConflict, Path: "Essay.md", KindFile: reconcile.KindFile,
		Hash: theirs.hash, Size: theirs.size, Vector: vector.Vector{"A": 1, "B": 1},
		ConflictPath: "Essay.synk-conflict-aaaaaaaa.md", ConflictKind: reconcile.KindFile,
		ConflictHash: mine.hash, ConflictSize: mine.size,
	}
	f.apply(op)
	if got := f.read("Essay.md"); got != "bbb" {
		t.Fatalf("winner %q", got)
	}
	if got := f.read(op.ConflictPath); got != "aaa" {
		t.Fatalf("conflict copy %q", got)
	}
	if f.idx.Entries[op.ConflictPath].Hash != mine.hash || f.idx.Entries["Essay.md"].Hash != theirs.hash {
		t.Fatal("index doesn't record both versions")
	}
	f.wantSnapshot("Essay.md", "aaa")
}

func TestApplyGivesFilesTheirExecutableBitAndTime(t *testing.T) {
	f := newFixture(t)
	when := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	hours := func(n time.Duration) int64 { return when.Add(n * time.Hour).UnixNano() }
	run := set("run.sh", f.blob("#!/bin/sh\n"))
	run.Meta = reconcile.Meta{Exec: true, MTime: hours(0), By: "B"}
	notes := set("notes.md", f.blob("hi"))
	notes.Meta = reconcile.Meta{MTime: hours(1), By: "B"}
	talk := set("Talk.key", f.manifest(map[string]string{"Index.zip": "idx", "Data/a.jpg": "jpg"}))
	talk.Meta = reconcile.Meta{MTime: hours(2), By: "B"}
	ours, theirs := f.file("essay.md", "ours"), f.blob("theirs")
	essay := reconcile.Op{
		Kind: reconcile.OpConflict, Path: "essay.md", KindFile: theirs.kind, Hash: theirs.hash, Size: theirs.size, Vector: vector.Vector{"A": 1, "B": 1},
		ConflictPath: "essay.synk-conflict-x.md", ConflictKind: ours.kind, ConflictHash: ours.hash, ConflictSize: ours.size,
		Meta: reconcile.Meta{MTime: hours(3), By: "B"}, ConflictMeta: reconcile.Meta{Exec: true, MTime: hours(4), By: "A"},
	}
	unknown := set("unknown.md", f.blob("no time"))
	f.apply(run, notes, talk, essay, unknown)
	for _, c := range []struct {
		rel  string
		mode fs.FileMode
		meta reconcile.Meta
	}{
		{"run.sh", 0o700, run.Meta},
		{"notes.md", 0o600, notes.Meta},
		{"Talk.key", fs.ModeDir | 0o700, talk.Meta},
		{"essay.md", 0o600, essay.Meta},
		{"essay.synk-conflict-x.md", 0o700, essay.ConflictMeta},
	} {
		st, err := os.Stat(f.abs(c.rel))
		must(t, err)
		if st.Mode() != c.mode || st.ModTime().UnixNano() != c.meta.MTime {
			t.Errorf("%s: mode %v, modified %v", c.rel, st.Mode(), st.ModTime())
		}
		if e := f.idx.Entries[c.rel]; e.Meta != c.meta {
			t.Errorf("%s: recorded %+v", c.rel, e.Meta)
		}
	}
	if st, err := os.Stat(f.abs("unknown.md")); err != nil || time.Since(st.ModTime()) > time.Hour {
		t.Errorf("a version without a time: %v, %v", st.ModTime(), err)
	}
}

func TestApplyChangesOnlyTheExecutableBit(t *testing.T) {
	f := newFixture(t)
	op := set("run.sh", f.file("run.sh", "#!/bin/sh\n"))
	must(t, os.Chmod(f.abs("run.sh"), 0o644))
	before, err := os.Stat(f.abs("run.sh"))
	must(t, err)
	last := fsmeta.FromInfo(before)
	for _, step := range []struct {
		exec, execBits bool
		mode           fs.FileMode
	}{
		{true, true, 0o755},
		{true, true, 0o755},
		{false, true, 0o644},
		{true, false, 0o644},
	} {
		// Linux keeps file times at the kernel's tick, a few milliseconds.
		time.Sleep(20 * time.Millisecond)
		f.env.execBits = step.execBits
		op.Meta.Exec = step.exec
		f.apply(op)
		st, err := os.Stat(f.abs("run.sh"))
		must(t, err)
		if st.Mode() != step.mode || f.idx.Entries["run.sh"].Exec != step.exec {
			t.Errorf("executable %v where the file system keeps the bit %v: mode %v, recorded %+v", step.exec, step.execBits, st.Mode(), f.idx.Entries["run.sh"])
		}
		if !os.SameFile(before, st) || !st.ModTime().Equal(before.ModTime()) {
			t.Error("replaced a file whose content didn't change")
		}
		now := fsmeta.FromInfo(st)
		if now.Mode == last.Mode && now != last {
			t.Errorf("changed a file that already had mode %v", st.Mode())
		}
		last = now
	}
	if vs, err := f.vault.Versions("lib", "run.sh"); err != nil || len(vs) > 0 {
		t.Errorf("kept versions %v, %v", vs, err)
	}

	f.env.execBits = true
	f.apply(set("Talk.key", f.pkg("Talk.key", map[string]string{"Index.zip": "idx"})))
	if st, err := os.Stat(f.abs("Talk.key")); err != nil || st.Mode() != fs.ModeDir|0o700 {
		t.Errorf("a package already in place: %v, %v", st.Mode(), err)
	}
}

func TestConflictCopyIsWrittenBeforeTheWinner(t *testing.T) {
	f := newFixture(t)
	mine := f.file("Essay.md", "mine")
	op := reconcile.Op{
		Kind: reconcile.OpConflict, Path: "Essay.md", KindFile: reconcile.KindFile,
		Hash: strings.Repeat("ab", 32), Size: 6, Vector: vector.Vector{"A": 1, "B": 1},
		ConflictPath: "Essay.synk-conflict-12345678.md", ConflictKind: reconcile.KindFile,
		ConflictHash: mine.hash, ConflictSize: mine.size,
	}
	if _, err := f.env.Apply([]reconcile.Op{op}, f.idx); err == nil {
		t.Fatal("applied a winner whose content is missing")
	}
	if got := f.read("Essay.md"); got != "mine" {
		t.Fatalf("original became %q", got)
	}
	if got := f.read(op.ConflictPath); got != "mine" {
		t.Fatalf("conflict copy %q", got)
	}
	f.wantNoStaging()
}

func TestNothingIsReplacedOrDeletedWithoutASnapshot(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs directory permissions to be enforced")
	}
	for name, op := range map[string]func(f *fixture) reconcile.Op{
		"overwrite": func(f *fixture) reconcile.Op { return set("a.md", f.blob("incoming")) },
		"delete":    func(f *fixture) reconcile.Op { return del("a.md") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.file("a.md", "only copy")
			versions := filepath.Join(f.vault.Root, "versions")
			must(t, os.Chmod(versions, 0o500))
			t.Cleanup(func() { _ = os.Chmod(versions, 0o700) })

			_, err := f.env.Apply([]reconcile.Op{op(f)}, f.idx)
			if err == nil || !strings.Contains(err.Error(), "vault snapshot failed") {
				t.Fatalf("err = %v", err)
			}
			if got := f.read("a.md"); got != "only copy" {
				t.Fatalf("a.md became %q although its vault snapshot failed", got)
			}
			f.wantNoStaging()
		})
	}
}

func TestApplyRefusesToOverwriteUnscannedEdit(t *testing.T) {
	for name, edit := range map[string]string{
		"different size": "typed after the scan",
		"same size":      "v2",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			scanned := f.file("a.md", "v1")
			f.write("a.md", edit)

			for _, op := range []reconcile.Op{set("a.md", f.blob("remote v2")), del("a.md")} {
				deferred, err := f.env.Apply([]reconcile.Op{op}, f.idx)
				must(t, err)
				if !slices.Equal(deferred, []string{"a.md"}) {
					t.Fatalf("%s: deferred %v", op.Kind, deferred)
				}
				if got := f.read("a.md"); got != edit {
					t.Fatalf("%s: an edit saved after the scan became %q", op.Kind, got)
				}
				if f.idx.Entries["a.md"].Hash != scanned.hash {
					t.Fatalf("%s: index changed for a skipped op", op.Kind)
				}
				f.wantNoStaging()
			}
		})
	}
}

func TestApplyRefusesToReplaceAFileItHasNotScanned(t *testing.T) {
	f := newFixture(t)
	f.write("new.md", "created here after the scan")
	deferred, err := f.env.Apply([]reconcile.Op{set("new.md", f.blob("created there"))}, f.idx)
	must(t, err)
	if !slices.Equal(deferred, []string{"new.md"}) {
		t.Fatalf("deferred %v", deferred)
	}
	if got := f.read("new.md"); got != "created here after the scan" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := newFixture(t)
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "target.md"), []byte("v1"), 0o600))
	must(t, os.Symlink(outside, f.abs("out")))
	must(t, os.Mkdir(f.abs("real"), 0o700))
	must(t, os.Symlink("real", f.abs("alias")))
	// The index is stale: a.md was a file holding "v1" when it was scanned.
	f.scanned("a.md", f.blob("v1"))
	must(t, os.Symlink(filepath.Join(outside, "target.md"), f.abs("a.md")))

	incoming := f.blob("incoming")
	for _, op := range []reconcile.Op{set("out/x.md", incoming), set("alias/x.md", incoming), del("alias/x.md")} {
		if _, err := f.env.Apply([]reconcile.Op{op}, f.idx); !errors.Is(err, ErrNotADir) {
			t.Fatalf("%s %s: err = %v", op.Kind, op.Path, err)
		}
	}
	for _, op := range []reconcile.Op{set("a.md", incoming), del("a.md")} {
		if deferred, err := f.env.Apply([]reconcile.Op{op}, f.idx); err != nil || len(deferred) != 1 {
			t.Fatalf("%s through a symlink: deferred %v, err %v", op.Kind, deferred, err)
		}
	}
	if got := tree(t, outside); len(got) != 1 || got["target.md"] != "v1" {
		t.Fatalf("outside the library: %v", got)
	}
	if got := tree(t, f.abs("real")); len(got) != 0 {
		t.Fatalf("wrote through a link inside the library: %v", got)
	}
	f.wantNoStaging()
}

func TestApplyPackages(t *testing.T) {
	newPkg := map[string]string{"Index.xml": "new", "Data/img.png": "png"}
	oldPkg := map[string]string{"Index.xml": "old", "old-only.txt": "x"}

	t.Run("create", func(t *testing.T) {
		f := newFixture(t)
		c := f.manifest(newPkg)
		f.apply(set("Doc.pages", c))
		if got := tree(t, f.abs("Doc.pages")); !maps.Equal(got, newPkg) {
			t.Fatalf("got %v", got)
		}
		if e := f.idx.Entries["Doc.pages"]; e.Kind != reconcile.KindPackage || !slices.Equal(e.Parts, c.parts) {
			t.Fatalf("index %+v", e)
		}
	})
	t.Run("replace a package", func(t *testing.T) {
		f := newFixture(t)
		f.pkg("Doc.pages", oldPkg)
		f.apply(set("Doc.pages", f.manifest(newPkg)))
		if got := tree(t, f.abs("Doc.pages")); !maps.Equal(got, newPkg) {
			t.Fatalf("got %v", got)
		}
		if got := tree(t, f.snapshot("Doc.pages")); !maps.Equal(got, oldPkg) {
			t.Fatalf("snapshot %v", got)
		}
	})
	t.Run("replace a file with a package", func(t *testing.T) {
		f := newFixture(t)
		f.file("Doc.pages", "was a file")
		f.apply(set("Doc.pages", f.manifest(newPkg)))
		if got := tree(t, f.abs("Doc.pages")); !maps.Equal(got, newPkg) {
			t.Fatalf("got %v", got)
		}
		f.wantSnapshot("Doc.pages", "was a file")
	})
	t.Run("replace a package with a file", func(t *testing.T) {
		f := newFixture(t)
		f.pkg("Doc.pages", oldPkg)
		f.apply(set("Doc.pages", f.blob("now a file")))
		if got := f.read("Doc.pages"); got != "now a file" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("delete", func(t *testing.T) {
		f := newFixture(t)
		f.pkg("Talks/Doc.pages", oldPkg)
		f.apply(del("Talks/Doc.pages"))
		if f.exists("Talks") {
			t.Fatal("package or its emptied folder left behind")
		}
		if got := tree(t, f.snapshot("Talks/Doc.pages")); !maps.Equal(got, oldPkg) {
			t.Fatalf("snapshot %v", got)
		}
	})
	t.Run("edited inside after the scan", func(t *testing.T) {
		f := newFixture(t)
		f.pkg("Doc.pages", oldPkg)
		f.write("Doc.pages/Index.xml", "edited")
		for _, op := range []reconcile.Op{set("Doc.pages", f.manifest(newPkg)), del("Doc.pages")} {
			deferred, err := f.env.Apply([]reconcile.Op{op}, f.idx)
			must(t, err)
			if len(deferred) != 1 {
				t.Fatalf("%s: deferred %v", op.Kind, deferred)
			}
		}
		if got := f.read("Doc.pages/Index.xml"); got != "edited" {
			t.Fatalf("got %q", got)
		}
		f.wantNoStaging()
	})
	t.Run("missing part", func(t *testing.T) {
		f := newFixture(t)
		f.pkg("Doc.pages", oldPkg)
		c := f.manifest(newPkg)
		h := c.parts[0]
		must(t, os.Remove(filepath.Join(f.store.Root, "objects", h[:2], h[2:4], h)))
		if _, err := f.env.Apply([]reconcile.Op{set("Doc.pages", c)}, f.idx); err == nil {
			t.Fatal("applied a package with missing parts")
		}
		if got := tree(t, f.abs("Doc.pages")); !maps.Equal(got, oldPkg) {
			t.Fatalf("got %v", got)
		}
		f.wantNoStaging()
	})
}

func TestMergeVectorOnlyChangesTheVector(t *testing.T) {
	f := newFixture(t)
	before := f.pkg("Doc.pages", map[string]string{"a": "1"})
	f.apply(reconcile.Op{Kind: reconcile.OpMergeVector, Path: "Doc.pages", Vector: vector.Vector{"A": 1, "B": 3}})
	e := f.idx.Entries["Doc.pages"]
	if e.Hash != before.hash || !slices.Equal(e.Parts, before.parts) || e.Vector["B"] != 3 {
		t.Fatalf("entry %+v", e)
	}
}

func TestApplyStopsWhenTheJournalFails(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs directory permissions to be enforced")
	}
	f := newFixture(t)
	f.file("a.md", "v1")
	dir := filepath.Dir(f.j.Path())
	must(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := f.env.Apply([]reconcile.Op{set("a.md", f.blob("v2")), set("b.md", f.blob("b"))}, f.idx)
	if !errors.Is(err, errJournal) {
		t.Fatalf("err = %v", err)
	}
	if f.read("a.md") != "v1" || f.exists("b.md") {
		t.Fatal("changed the library without a journal")
	}
}

func TestOpenRequiresTheLibraryMarker(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	if _, err := Open(dir, "lib", f.store, f.vault, f.j); !errors.Is(err, ErrNoMarker) {
		t.Fatalf("no marker: err = %v", err)
	}
	if runtime.GOOS != "windows" {
		must(t, os.Symlink(t.TempDir(), filepath.Join(dir, ".synk")))
		if _, err := Open(dir, "lib", f.store, f.vault, f.j); !errors.Is(err, ErrNoMarker) {
			t.Fatalf("symlinked marker: err = %v", err)
		}
	}
}

func TestApplyRejectsUnsafePaths(t *testing.T) {
	f := newFixture(t)
	c := f.blob("x")
	for _, rel := range []string{"../escape.md", "/abs.md", ".synk/staging/x", "a/../../b"} {
		if _, err := f.env.Apply([]reconcile.Op{set(rel, c)}, f.idx); err == nil {
			t.Fatalf("%q applied", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.root), "escape.md")); err == nil {
		t.Fatal("wrote outside the library")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
