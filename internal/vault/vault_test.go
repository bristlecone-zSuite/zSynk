package vault

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAndPrune(t *testing.T) {
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v.Keep = 3
	// Another snapshot of the path is being written.
	writing := filepath.Join(v.PathDir("lib", "notes/a.md"), tmpPrefix+"writing")
	must(t, os.MkdirAll(writing, 0o700))
	var last *EntryMeta
	for i := 0; i < 5; i++ {
		last, err = v.SnapshotFile("lib", "notes/a.md", strings.NewReader(string(rune('a'+i))), "h", "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	ents, err := os.ReadDir(v.PathDir("lib", "notes/a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 4 {
		t.Fatalf("keep 3 and the snapshot being written, got %d", len(ents))
	}
	if _, err := os.Stat(writing); err != nil {
		t.Fatalf("removed a snapshot being written: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(last.Dir, "content"))
	if err != nil || string(got) != "e" {
		t.Fatalf("newest version holds %q (%v), want %q", got, err, "e")
	}
	raw, err := os.ReadFile(filepath.Join(last.Dir, "meta.json"))
	if err != nil || !strings.Contains(string(raw), `"path": "notes/a.md"`) {
		t.Fatalf("meta.json doesn't record the path: %s (%v)", raw, err)
	}
}

// Versions lists a path's versions newest first, List does for a whole
// library, and Read reads one.
func TestVersions(t *testing.T) {
	v := open(t)
	snap(t, v, "notes/a.md", "first", "pre-overwrite")
	snap(t, v, "b.md", "only", "pre-delete")
	snap(t, v, "notes/a.md", "second!", "pre-overwrite")
	pkg := filepath.Join(t.TempDir(), "Talk.key")
	write(t, filepath.Join(pkg, "Data", "img.png"), "png")
	write(t, filepath.Join(pkg, "Index.zip"), "index")
	if _, err := v.SnapshotTree("lib", "Talk.key", os.DirFS(pkg), "tree-hash", "pre-delete"); err != nil {
		t.Fatal(err)
	}

	got, err := v.Versions("lib", "notes/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Size != 7 || got[1].Size != 5 || got[0].Path != "notes/a.md" || got[0].Kind != "file" ||
		got[0].Reason != "pre-overwrite" || got[0].Hash != "hash" || !got[0].At.After(got[1].At) || got[0].ID == got[1].ID {
		t.Fatalf("versions of notes/a.md: %+v", got)
	}
	all, err := v.List("lib")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, ver := range all {
		paths = append(paths, ver.Path)
	}
	if want := []string{"Talk.key", "notes/a.md", "b.md", "notes/a.md"}; !slices.Equal(paths, want) {
		t.Fatalf("List: %q; want %q", paths, want)
	}
	if all[0].Kind != "package" || all[0].Size != 8 {
		t.Fatalf("the package's version: %+v", all[0])
	}
	if none, err := v.List("other"); err != nil || len(none) != 0 || none == nil {
		t.Fatalf("List of a library with no versions: %#v, %v", none, err)
	}

	err = v.Read("lib", "notes/a.md", got[1].ID, func(ver Version, content string) error {
		b, err := os.ReadFile(content)
		if err != nil || string(b) != "first" || ver != got[1] {
			t.Errorf("read %+v holding %q (%v)", ver, b, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = v.Read("lib", "Talk.key", all[0].ID, func(_ Version, content string) error {
		b, err := os.ReadFile(filepath.Join(content, "Data", "img.png"))
		if err != nil || string(b) != "png" {
			t.Errorf("read the package's part as %q (%v)", b, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(rel, id string) error {
		return v.Read("lib", rel, id, func(Version, string) error {
			t.Errorf("read version %q of %s", id, rel)
			return nil
		})
	}
	// What Prune is removing is no longer a version.
	dir := v.PathDir("lib", "notes/a.md")
	must(t, os.CopyFS(filepath.Join(dir, gonePrefix+got[1].ID), os.DirFS(filepath.Join(dir, got[1].ID))))
	if vs, err := v.Versions("lib", "notes/a.md"); err != nil || len(vs) != 2 {
		t.Fatalf("with a version being removed, notes/a.md's are %+v, %v", vs, err)
	}
	for _, id := range []string{"", "..", "../b.md", got[0].ID + "-../../x", got[0].ID + "/..", "meta.json", gonePrefix + got[1].ID} {
		if err := read("notes/a.md", id); !errors.Is(err, ErrNoVersion) {
			t.Errorf("Read of version %q: %v", id, err)
		}
	}
	if err := read("b.md", got[0].ID); !errors.Is(err, ErrNoVersion) {
		t.Errorf("Read of another path's version: %v", err)
	}
}

// Prune removes versions older than MaxAge, except each path's newest,
// and quarantined content as old.
func TestPruneByAge(t *testing.T) {
	v := open(t)
	snap(t, v, "a.md", "one", "pre-overwrite")
	snap(t, v, "a.md", "two", "pre-overwrite")
	snap(t, v, "a.md", "three", "pre-overwrite")
	if _, err := v.Quarantine("bad", []byte("junk"), "test"); err != nil {
		t.Fatal(err)
	}
	// Without a time in its name, what's in quarantine is as old as its file.
	write(t, filepath.Join(v.Root, "quarantine", "unnamed"), "?")
	// What isn't a version is left alone, and doesn't count as the newest.
	unknown := filepath.Join(v.PathDir("lib", "a.md"), "unknown")
	write(t, filepath.Join(unknown, "content"), "?")
	removed, freed, err := v.Prune(time.Now().Add(v.MaxAge - time.Hour))
	if err != nil || removed != 0 || freed != 0 {
		t.Fatalf("pruning before MaxAge: removed %d (%d bytes), %v", removed, freed, err)
	}
	removed, freed, err = v.Prune(time.Now().Add(v.MaxAge + time.Hour))
	if err != nil || removed != 4 || freed != int64(len("one")+len("two")+len("junk")+len("test")+len("?")) {
		t.Fatalf("pruning after MaxAge: removed %d (%d bytes), %v", removed, freed, err)
	}
	if got := contents(t, v, "a.md"); !slices.Equal(got, []string{"three"}) {
		t.Fatalf("a.md keeps %q", got)
	}
	if ents, _ := os.ReadDir(filepath.Join(v.Root, "quarantine")); len(ents) != 0 {
		t.Fatalf("quarantine keeps %d items", len(ents))
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("Prune removed a folder that isn't a version: %v", err)
	}
}

// Over MaxBytes, Prune removes quarantined content first, then versions
// other than each path's newest, and those last, oldest first each time.
func TestPruneBySize(t *testing.T) {
	v := open(t)
	hundred := strings.Repeat("x", 100)
	snap(t, v, "b.md", hundred, "pre-overwrite")
	snap(t, v, "a.md", hundred, "pre-overwrite")
	snap(t, v, "a.md", hundred, "pre-overwrite")
	if _, err := v.Quarantine("bad", []byte(hundred), ""); err != nil {
		t.Fatal(err)
	}
	prune := func(maxBytes int64, removed, a, b, quarantined int) {
		t.Helper()
		v.MaxBytes = maxBytes
		got, freed, err := v.Prune(time.Now())
		if err != nil || got != removed || freed != int64(100*removed) {
			t.Fatalf("pruning to %d bytes removed %d (%d bytes), %v; want %d", maxBytes, got, freed, err, removed)
		}
		q, err := os.ReadDir(filepath.Join(v.Root, "quarantine"))
		must(t, err)
		if gotA, gotB := len(contents(t, v, "a.md")), len(contents(t, v, "b.md")); gotA != a || gotB != b || len(q) != quarantined {
			t.Fatalf("pruning to %d bytes left %d versions of a.md, %d of b.md and %d quarantined; want %d, %d and %d",
				maxBytes, gotA, gotB, len(q), a, b, quarantined)
		}
	}
	prune(400, 0, 2, 1, 1)
	prune(350, 1, 2, 1, 0)
	// b.md's only version is the oldest, but a.md has another.
	prune(250, 1, 1, 1, 0)
	prune(150, 1, 1, 0, 0)
	if _, err := os.Stat(v.PathDir("lib", "b.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("b.md's empty folder is left: %v", err)
	}
	prune(50, 1, 0, 0, 0)
	if _, err := os.Stat(filepath.Join(v.Root, "versions", "lib")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the library's empty folder is left: %v", err)
	}
}

// Lowering Keep removes the oldest versions over it.
func TestPruneByCount(t *testing.T) {
	v := open(t)
	for _, s := range []string{"one", "two", "three"} {
		snap(t, v, "a.md", s, "pre-overwrite")
	}
	v.Keep = 2
	if removed, freed, err := v.Prune(time.Now()); err != nil || removed != 1 || freed != int64(len("one")) {
		t.Fatalf("removed %d (%d bytes), %v", removed, freed, err)
	}
	if got := contents(t, v, "a.md"); !slices.Equal(got, []string{"three", "two"}) {
		t.Fatalf("a.md keeps %q", got)
	}
}

// Prune removes what interrupted work left, except snapshots that may
// still be under way.
func TestPruneRemovesLeftovers(t *testing.T) {
	v := open(t)
	snap(t, v, "a.md", "kept", "pre-overwrite")
	dir := v.PathDir("lib", "a.md")
	old := time.Now().Add(-2 * tmpGrace)
	for _, name := range []string{tmpPrefix + "crashed", tmpPrefix + "writing", gonePrefix + "removing"} {
		write(t, filepath.Join(dir, name, "content"), "12345")
	}
	must(t, os.Chtimes(filepath.Join(dir, tmpPrefix+"crashed"), old, old))
	write(t, filepath.Join(v.Root, "quarantine", gonePrefix+"removing", "payload"), "12345")
	removed, freed, err := v.Prune(time.Now())
	if err != nil || removed != 0 || freed != 15 {
		t.Fatalf("removed %d (%d bytes), %v", removed, freed, err)
	}
	ents, err := os.ReadDir(dir)
	must(t, err)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != tmpPrefix+"writing" || !versionID(names[1]) {
		t.Fatalf("left %q", names)
	}
}

// Limits left at zero are the defaults, not "keep nothing".
func TestZeroLimitsMeanDefaults(t *testing.T) {
	v := open(t)
	v.Keep, v.MaxAge, v.MaxBytes = 0, 0, 0
	snap(t, v, "a.md", "one", "pre-overwrite")
	snap(t, v, "a.md", "two", "pre-overwrite")
	if removed, _, err := v.Prune(time.Now().Add(time.Hour)); err != nil || removed != 0 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	if got := contents(t, v, "a.md"); !slices.Equal(got, []string{"two", "one"}) {
		t.Fatalf("a.md keeps %q", got)
	}
}

// Two paths whose hashes share a folder don't get each other's versions.
func TestVersionsOfAnotherPath(t *testing.T) {
	v := open(t)
	snap(t, v, "a.md", "a's", "pre-overwrite")
	vs, err := v.Versions("lib", "a.md")
	must(t, err)
	must(t, os.CopyFS(v.PathDir("lib", "b.md"), os.DirFS(v.PathDir("lib", "a.md"))))
	if got, err := v.Versions("lib", "b.md"); err != nil || len(got) != 0 {
		t.Fatalf("b.md's versions: %+v, %v", got, err)
	}
	err = v.Read("lib", "b.md", vs[0].ID, func(Version, string) error {
		t.Error("read a.md's version as b.md's")
		return nil
	})
	if !errors.Is(err, ErrNoVersion) {
		t.Fatalf("Read: %v", err)
	}
}

func TestVersionID(t *testing.T) {
	const stamp = "20261007T120000.000000001Z"
	for name, want := range map[string]bool{
		stamp: true, stamp + "-1": true, stamp + "-12": true,
		"": false, "content": false, "meta.json": false, "20261007T120000Z": false,
		stamp + "-": false, stamp + "-x": false, stamp + "-1-2": false, stamp + "-../x": false,
		tmpPrefix + stamp: false, gonePrefix + stamp: false,
	} {
		if got := versionID(name); got != want {
			t.Errorf("versionID(%q) = %v", name, got)
		}
	}
}

// A version being read stays until the read ends.
func TestPruneWaitsForReads(t *testing.T) {
	v := open(t)
	snap(t, v, "a.md", "old", "pre-overwrite")
	snap(t, v, "a.md", "new", "pre-overwrite")
	vs, err := v.Versions("lib", "a.md")
	must(t, err)
	pruned := make(chan struct{})
	err = v.Read("lib", "a.md", vs[1].ID, func(_ Version, content string) error {
		go func() {
			defer close(pruned)
			_, _, _ = v.Prune(time.Now().Add(v.MaxAge + time.Hour))
		}()
		time.Sleep(50 * time.Millisecond)
		if b, err := os.ReadFile(content); err != nil || string(b) != "old" {
			t.Errorf("the version went while it was read: %q, %v", b, err)
		}
		return nil
	})
	must(t, err)
	<-pruned
	if got := contents(t, v, "a.md"); !slices.Equal(got, []string{"new"}) {
		t.Fatalf("after the read, a.md keeps %q", got)
	}
}

func open(t *testing.T) *Vault {
	t.Helper()
	v, err := Open(t.TempDir())
	must(t, err)
	return v
}

func snap(t *testing.T, v *Vault, rel, content, reason string) {
	t.Helper()
	if _, err := v.SnapshotFile("lib", rel, strings.NewReader(content), "hash", reason); err != nil {
		t.Fatal(err)
	}
}

// contents is what each version of rel holds, newest first.
func contents(t *testing.T, v *Vault, rel string) []string {
	t.Helper()
	vs, err := v.Versions("lib", rel)
	must(t, err)
	out := []string{}
	for _, ver := range vs {
		must(t, v.Read("lib", rel, ver.ID, func(_ Version, content string) error {
			b, err := os.ReadFile(content)
			out = append(out, string(b))
			return err
		}))
	}
	return out
}

func write(t *testing.T, p, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	must(t, os.WriteFile(p, []byte(content), 0o600))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotTreeSkipsSymlinks(t *testing.T) {
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "Talk.key")
	if err := os.MkdirAll(filepath.Join(pkg, "Data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "Data", "img.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(pkg, "etc")); err != nil {
		t.Fatal(err)
	}
	meta, err := v.SnapshotTree("lib", "Talk.key", os.DirFS(pkg), "h", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(meta.Dir, "content", "Data", "img.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(meta.Dir, "content", "etc")); err == nil {
		t.Fatal("a symlink inside the package was copied into the vault")
	}
}

func TestQuarantine(t *testing.T) {
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := v.Quarantine("bad", []byte("nope"), "hash mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "payload")); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineNameStaysInQuarantine(t *testing.T) {
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(v.Root, "quarantine")
	for _, name := range []string{"../../escape", "/etc/passwd", "..", "", "a/b\\c\x00d"} {
		dir, err := v.Quarantine(name, []byte("x"), "test")
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if filepath.Dir(dir) != parent {
			t.Fatalf("%q was quarantined at %s, outside %s", name, dir, parent)
		}
	}
	a, err := v.Quarantine("same", []byte("1"), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Quarantine("same", []byte("2"), "")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two quarantines shared a directory")
	}
}
