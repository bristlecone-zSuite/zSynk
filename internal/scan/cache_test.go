package scan

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
)

// A scan reads only files that changed since the last one. Each case plants
// a cache entry for a.md whose content is another blob in the store: a scan
// that trusts the entry reports that blob, and one that reads the file
// reports the file's own.
func TestCacheReadsOnlyWhatChanged(t *testing.T) {
	root := library(t)
	write(t, root, "a.md", "hello")
	store, err := blob.New(t.TempDir())
	must(t, err)
	planted, err := store.Put([]byte("planted"))
	must(t, err)
	own := atomicfile.HashBytes([]byte("hello"))
	fi, err := os.Lstat(filepath.Join(root, "a.md"))
	must(t, err)
	st := fsmeta.FromInfo(fi)
	grown := st
	grown.Size++
	real, err := filepath.EvalSymlinks(root)
	must(t, err)

	for name, c := range map[string]struct {
		root  string
		entry Cached
		want  string
	}{
		"unchanged":          {real, Cached{Stat: st, Hash: planted, Size: 5}, planted},
		"other attributes":   {real, Cached{Stat: grown, Hash: planted, Size: 5}, own},
		"content not stored": {real, Cached{Stat: st, Hash: atomicfile.HashBytes([]byte("gone")), Size: 5}, own},
		"another folder's":   {t.TempDir(), Cached{Stat: st, Hash: planted, Size: 5}, own},
		"a package's":        {real, Cached{Stat: st, Tree: "x", Hash: planted, Size: 5}, own},
	} {
		cache := &Cache{Root: c.root, Entries: map[string]Cached{"a.md": c.entry}}
		res, err := Library(root, Options{Cache: cache}, store)
		must(t, err)
		if got := items(res)["a.md"].Hash; got != c.want {
			t.Errorf("%s: scanned %s; want %s", name, got, c.want)
		}
		if got := cache.Entries["a.md"]; got.Hash != c.want || cache.Root != real {
			t.Errorf("%s: the cache has %+v for %s", name, got, cache.Root)
		}
	}
}

// A file that changed shortly before a scan is read again by the next one:
// its attributes might not show a write in the same clock tick as the read.
func TestCacheRereadsRecentChanges(t *testing.T) {
	root := library(t)
	write(t, root, "a.md", "hello")
	store, err := blob.New(t.TempDir())
	must(t, err)
	cache := &Cache{}
	_, err = Library(root, Options{Cache: cache, Racy: time.Hour}, store)
	must(t, err)
	if got, ok := cache.Entries["a.md"]; ok {
		t.Fatalf("cached %+v, which changed within the racy window", got)
	}
	_, err = Library(root, Options{Cache: cache}, store)
	must(t, err)
	if got := cache.Entries["a.md"]; got.Hash != atomicfile.HashBytes([]byte("hello")) {
		t.Fatalf("cached %+v", got)
	}

	// An app can set a file's mtime to anything; the system sets its ctime.
	p := filepath.Join(root, "b.md")
	write(t, root, "b.md", "backdated")
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, os.Chtimes(p, old, old))
	if fi, err := os.Lstat(p); err != nil || fsmeta.FromInfo(fi).CTime == 0 {
		t.Skip("no ctime on this system")
	}
	_, err = Library(root, Options{Cache: cache, Racy: time.Hour}, store)
	must(t, err)
	if got, ok := cache.Entries["b.md"]; ok {
		t.Fatalf("cached %+v, which changed just now with an old mtime", got)
	}
}

// Changed says when there's something new to save.
func TestCacheChanged(t *testing.T) {
	root := library(t)
	write(t, root, "a.md", "hello")
	write(t, root, "b.md", "world")
	store, err := blob.New(t.TempDir())
	must(t, err)
	cache := &Cache{}
	for i, step := range []struct {
		change func()
		want   bool
	}{
		{func() {}, true},
		{func() {}, false},
		{func() { must(t, os.Remove(filepath.Join(root, "b.md"))) }, true},
		{func() { write(t, root, "a.md", "edited") }, true},
		{func() {}, false},
	} {
		step.change()
		_, err := Library(root, Options{Cache: cache}, store)
		must(t, err)
		if cache.Changed() != step.want {
			t.Fatalf("step %d: Changed() = %v", i, !step.want)
		}
	}
	if len(cache.Entries) != 1 || cache.Entries["a.md"].Hash != atomicfile.HashBytes([]byte("edited")) {
		t.Fatalf("entries %+v", cache.Entries)
	}
}

// Capturing a package again copies none of the parts the store has.
func TestPackagesStoreOnlyNewParts(t *testing.T) {
	root := library(t)
	write(t, root, "Doc.pages/Index.zip", "idx")
	write(t, root, "Doc.pages/Data/a.png", "png")
	res, store := run(t, root, Options{})
	first := items(res)["Doc.pages"]
	if os.Geteuid() == 0 {
		t.Skip("root can write to the store's temporary folder")
	}
	// With nowhere to put a copy, only parts the store has can be captured.
	tmp := filepath.Join(store.Root, "tmp")
	must(t, os.Chmod(tmp, 0o500))
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	again, err := Library(root, Options{}, store)
	must(t, err)
	if got := items(again)["Doc.pages"]; got.Hash != first.Hash || got.Incomplete || len(again.Errors) != 0 {
		t.Fatalf("capturing the package again: %+v, %v", got, again.Errors)
	}
}

func TestCacheReusesUnchangedPackages(t *testing.T) {
	root := library(t)
	write(t, root, "Doc.pages/Index.zip", "idx")
	write(t, root, "Doc.pages/Data/a.png", "png")
	store, err := blob.New(t.TempDir())
	must(t, err)
	cache := &Cache{}
	scanned := func() Cached {
		t.Helper()
		res, err := Library(root, Options{Cache: cache}, store)
		must(t, err)
		it := items(res)["Doc.pages"]
		return Cached{Hash: it.Hash, Size: it.Size, Parts: it.Parts}
	}
	first := scanned()
	c := cache.Entries["Doc.pages"]
	if c.Tree == "" || c.Hash != first.Hash || !slices.Equal(c.Parts, first.Parts) {
		t.Fatalf("cached %+v for %+v", c, first)
	}

	planted, err := store.Put([]byte("planted manifest"))
	must(t, err)
	c.Hash = planted
	cache.Entries["Doc.pages"] = c
	if got := scanned(); got.Hash != planted || got.Size != first.Size || !slices.Equal(got.Parts, first.Parts) {
		t.Fatalf("read an unchanged package again: %+v", got)
	}
	c.Parts = append(slices.Clone(first.Parts), atomicfile.HashBytes([]byte("not stored")))
	cache.Entries["Doc.pages"] = c
	if got := scanned(); got.Hash != first.Hash || !slices.Equal(got.Parts, first.Parts) {
		t.Fatalf("reused a package whose parts aren't all stored: %+v", got)
	}
	write(t, root, "Doc.pages/Data/a.png", "png, edited")
	if got := scanned(); got.Hash == first.Hash || got.Size != first.Size+8 {
		t.Fatalf("a package with an edited part scanned as %+v", got)
	}
}
