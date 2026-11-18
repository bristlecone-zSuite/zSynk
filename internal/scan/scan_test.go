package scan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
)

func library(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must(t, os.Mkdir(filepath.Join(root, ".synk"), 0o700))
	return root
}

func write(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	must(t, os.MkdirAll(filepath.Dir(p), 0o700))
	must(t, os.WriteFile(p, []byte(data), 0o600))
}

func run(t *testing.T, root string, opts Options) (*Result, *blob.Store) {
	t.Helper()
	store, err := blob.New(t.TempDir(), nil)
	must(t, err)
	res, err := Library(root, opts, store)
	must(t, err)
	return res, store
}

func items(res *Result) map[string]reconcile.ScanItem {
	out := map[string]reconcile.ScanItem{}
	for _, it := range res.Items {
		out[it.Path] = it
	}
	return out
}

func TestScanFilesAndPackage(t *testing.T) {
	root := library(t)
	write(t, root, "a.md", "hello")
	write(t, root, ".DS_Store", "x")
	write(t, root, ".obsidian/cache/x", "x")
	write(t, root, "Doc.pages/Index.zip", "idx")
	write(t, root, "Doc.pages/Data/a.png", "png")
	write(t, root, "Doc.pages/.DS_Store", "x")
	write(t, root, ".synk/staging/tx/new", "staged")
	res, _ := run(t, root, Options{Ignore: Ignorer([]string{".obsidian/cache/"})})
	got := items(res)
	if len(got) != 2 {
		t.Fatalf("items %+v", res.Items)
	}
	if it := got["a.md"]; it.Kind != reconcile.KindFile || it.Hash != atomicfile.HashBytes([]byte("hello")) {
		t.Fatalf("%+v", it)
	}
	if it := got["Doc.pages"]; it.Kind != reconcile.KindPackage || it.Size != 6 || len(it.Parts) != 2 || it.Incomplete {
		t.Fatalf("%+v", it)
	}
}

func TestScanReportsExecutableBitsAndModificationTimes(t *testing.T) {
	root := library(t)
	write(t, root, "run.sh", "#!/bin/sh\n")
	must(t, os.Chmod(filepath.Join(root, "run.sh"), 0o755))
	write(t, root, "notes.md", "hi")
	write(t, root, "Talk.key/Index.zip", "idx")
	write(t, root, "Talk.key/Data/a.jpg", "jpg")
	at := func(rel string, sec int64) {
		must(t, os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), time.Time{}, time.Unix(sec, 0)))
	}
	at("run.sh", 1000)
	at("notes.md", 2000)
	at("Talk.key/Index.zip", 3000)
	at("Talk.key/Data/a.jpg", 4000)
	at("Talk.key/Data", 1000)
	at("Talk.key", 1500)
	want := map[string]reconcile.ScanItem{
		"run.sh":   {Exec: true, MTime: 1000e9},
		"notes.md": {MTime: 2000e9},
		"Talk.key": {MTime: 4000e9},
	}
	store, err := blob.New(t.TempDir(), nil)
	must(t, err)
	cache := &Cache{}
	for _, scan := range []string{"first", "cached"} {
		res, err := Library(root, Options{Cache: cache}, store)
		must(t, err)
		got := items(res)
		for p, w := range want {
			if got[p].Exec != w.Exec || got[p].MTime != w.MTime {
				t.Errorf("%s scan: %s is %+v", scan, p, got[p])
			}
		}
		if scan == "cached" && cache.Changed() {
			t.Error("the second scan read files again")
		}
	}
}

func TestScanRequiresTheLibraryMarker(t *testing.T) {
	store, err := blob.New(t.TempDir(), nil)
	must(t, err)
	if _, err := Library(t.TempDir(), Options{}, store); !errors.Is(err, ErrNoMarker) {
		t.Fatalf("err = %v", err)
	}
}

func TestScanFollowsASymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := library(t)
	write(t, root, "a.md", "hello")
	link := filepath.Join(t.TempDir(), "notes")
	must(t, os.Symlink(root, link))
	res, _ := run(t, link, Options{})
	if _, ok := items(res)["a.md"]; !ok {
		t.Fatalf("items %+v", res.Items)
	}
}

func TestScanReportsWhatItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs directory permissions to be enforced")
	}
	root := library(t)
	write(t, root, "Projects/plan.md", "plan")
	write(t, root, "ok.md", "ok")
	sub := filepath.Join(root, "Projects")
	must(t, os.Chmod(sub, 0o000))
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	res, _ := run(t, root, Options{})
	if !slices.Contains(res.Unconfirmed, "Projects") || len(res.Errors) == 0 {
		t.Fatalf("unconfirmed %v, errors %v", res.Unconfirmed, res.Errors)
	}
	if _, ok := items(res)["ok.md"]; !ok {
		t.Fatal("one unreadable folder stopped the scan")
	}

	must(t, os.Chmod(root, 0o300))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	store, err := blob.New(t.TempDir(), nil)
	must(t, err)
	if _, err := Library(root, Options{}, store); err == nil {
		t.Fatal("scanned a root it couldn't list")
	}
}

func TestPlaceholders(t *testing.T) {
	root := library(t)
	write(t, root, "notes/.todo.md.icloud", "bplist")
	write(t, root, "notes/Talk.key.icloud", "bplist")
	write(t, root, ".both.md.icloud", "bplist")
	write(t, root, "both.md", "downloaded")
	write(t, root, ".obsidian/.workspace.json.icloud", "bplist")
	res, _ := run(t, root, Options{Ignore: Ignorer([]string{".obsidian/workspace.json"})})
	got := items(res)
	if it := got["notes/todo.md"]; !it.Incomplete || it.Kind != reconcile.KindFile {
		t.Fatalf("todo: %+v", got)
	}
	if it := got["notes/Talk.key"]; !it.Incomplete || it.Kind != reconcile.KindPackage {
		t.Fatalf("talk: %+v", got)
	}
	if it := got["both.md"]; it.Incomplete || it.Hash == "" {
		t.Fatalf("a downloaded file lost to its stale placeholder: %+v", it)
	}
	if len(got) != 3 {
		t.Fatalf("items %+v", res.Items)
	}
}

func TestScanSkipsLiveAppLibraries(t *testing.T) {
	root := library(t)
	write(t, root, "Photos Library.photoslibrary/database/Photos.sqlite", "db")
	write(t, root, "a.md", "a")
	res, _ := run(t, root, Options{})
	if len(res.Items) != 1 || !slices.Contains(res.Unconfirmed, "Photos Library.photoslibrary") {
		t.Fatalf("items %+v, unconfirmed %v", res.Items, res.Unconfirmed)
	}
}

func TestPackagesMustSettle(t *testing.T) {
	root := library(t)
	write(t, root, "Talk.key/Index.zip", "idx")
	res, _ := run(t, root, Options{Settle: time.Hour})
	if it := items(res)["Talk.key"]; !it.Incomplete || !slices.Contains(res.Busy, "Talk.key") {
		t.Fatalf("captured a package that was just written: %+v", res)
	}
	res, _ = run(t, root, Options{})
	if it := items(res)["Talk.key"]; it.Incomplete || it.Hash == "" {
		t.Fatalf("%+v", it)
	}
}

func TestPackageWithPartsInICloudIsIncomplete(t *testing.T) {
	root := library(t)
	write(t, root, "Talk.key/.Index.zip.icloud", "bplist")
	write(t, root, "Talk.key/Data/a.png", "png")
	res, _ := run(t, root, Options{})
	if it := items(res)["Talk.key"]; !it.Incomplete || len(res.Errors) == 0 {
		t.Fatalf("captured a package missing a part: %+v", res)
	}
}

func TestDatalessFilesAreNotRead(t *testing.T) {
	root := library(t)
	write(t, root, "evicted.md", "content only in the cloud")
	write(t, root, "Evicted Folder/a.md", "a")
	write(t, root, "local.md", "local")
	isDataless = func(fi fs.FileInfo) bool { return fi.Name() == "evicted.md" || fi.Name() == "Evicted Folder" }
	t.Cleanup(func() { isDataless = datalessFlag })

	res, store := run(t, root, Options{})
	got := items(res)
	if it := got["evicted.md"]; !it.Incomplete || !slices.Contains(res.Placeholders, "evicted.md") {
		t.Fatalf("evicted.md: %+v", it)
	}
	if store.Has(atomicfile.HashBytes([]byte("content only in the cloud"))) {
		t.Fatal("read a dataless file, which downloads it")
	}
	if _, ok := got["Evicted Folder/a.md"]; ok || !slices.Contains(res.Unconfirmed, "Evicted Folder") {
		t.Fatalf("listed a dataless folder: %+v", res)
	}
	if _, ok := got["local.md"]; !ok {
		t.Fatal("missing local.md")
	}
}

func TestUnsupportedNamesAreUnconfirmed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a separator on Windows")
	}
	root := library(t)
	write(t, root, `back\slash.md`, "x")
	res, _ := run(t, root, Options{})
	if len(res.Items) != 0 || !slices.Contains(res.Unconfirmed, `back\slash.md`) {
		t.Fatalf("items %+v, unconfirmed %v", res.Items, res.Unconfirmed)
	}
}

func TestPlaceholderTarget(t *testing.T) {
	for in, want := range map[string]string{
		".note.md.icloud":        "note.md",
		"a/b/.Doc.pages.icloud":  "a/b/Doc.pages",
		"a/..hidden.icloud":      "a/.hidden",
		"note.md.icloud":         "note.md",
		"a/.Note.md.ICLOUD":      "a/Note.md",
		".icloud":                "",
		"a/note.md":              "",
		"icloud":                 "",
		"a/.icloud/plain.md":     "",
		"a/report.icloud.backup": "",
	} {
		got, ok := placeholderTarget(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q, %v; want %q", in, got, ok, want)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
