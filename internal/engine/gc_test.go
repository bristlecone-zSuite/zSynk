package engine

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

// The store keeps content an index references, content a session in
// progress needs, and anything stored within gcGrace. It drops the rest.
func TestCollectGarbage(t *testing.T) {
	n := openNode(t, "Mac")
	dir := library(t, n, "notes")
	writeFile(t, dir, "a.md", "first")
	writeFile(t, dir, "gone.md", "deleted")
	writeFile(t, dir, "Talk.key/Index.zip", "index")
	writeFile(t, dir, "Talk.key/Data/movie.mov", "movie")
	_, err := n.ScanLibrary("notes")
	must(t, err)
	writeFile(t, dir, "a.md", "second")
	must(t, os.Remove(filepath.Join(dir, "gone.md")))
	_, err = n.ScanLibrary("notes")
	must(t, err)
	pinned, err := n.Store.Put([]byte("a sync needs this"))
	must(t, err)
	agedStore(t, n)
	recent, err := n.Store.Put([]byte("stored just now"))
	must(t, err)

	remote := reconcile.NewIndex("notes", "PEER")
	remote.Entries["b.md"] = reconcile.Entry{Path: "b.md", Kind: reconcile.KindFile, Hash: pinned, Size: 17, Vector: vector.Vector{"PEER": 1}}
	_, release := n.Wants(n.Index("notes"), remote)
	collect := func() {
		t.Helper()
		_, _, err := n.CollectGarbage()
		must(t, err)
	}
	collect()
	if n.Store.Has(atomicfile.HashBytes([]byte("first"))) {
		t.Error("kept content no index references")
	}
	if n.Store.Has(atomicfile.HashBytes([]byte("deleted"))) {
		t.Error("kept a deleted file's content")
	}
	pkg := n.Index("notes").Entries["Talk.key"]
	for name, h := range map[string]string{
		"a file's content":        atomicfile.HashBytes([]byte("second")),
		"a package's manifest":    pkg.Hash,
		"a package's first part":  pkg.Parts[0],
		"a package's last part":   pkg.Parts[len(pkg.Parts)-1],
		"content a sync needs":    pinned,
		"content stored just now": recent,
	} {
		if !n.Store.Has(h) {
			t.Errorf("removed %s", name)
		}
	}
	release()
	collect()
	if n.Store.Has(pinned) {
		t.Error("kept content a finished sync needed")
	}
}

// A sync keeps what it needs only while it runs.
func TestSessionsReleaseWhatTheyNeed(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "a.md", "from A")
	writeFile(t, dirB, "b.md", "from B")
	must(t, b.SyncWith(addrA, "notes"))
	for _, n := range []*Node{a, b} {
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			var pinned int
			locked(n, func() { pinned = len(n.pins) })
			if pinned == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still keeps %d blobs for a finished sync", n.Device.Name, pinned)
			}
		}
	}
}

// The loop removes what the store doesn't need.
func TestLoopCollectsGarbage(t *testing.T) {
	tick(t, 20*time.Millisecond)
	n := openNode(t, "Mac")
	rec := &recorder{}
	n.Log = slog.New(rec)
	unused, err := n.Store.Put([]byte("unused"))
	must(t, err)
	agedStore(t, n)
	n.Vault.MaxBytes = 1
	_, err = n.Vault.SnapshotFile("notes", "a.md", strings.NewReader("old"), atomicfile.HashBytes([]byte("old")), "pre-overwrite")
	must(t, err)
	run(t, n)
	kept := func() bool {
		vs, err := n.Vault.Versions("notes", "a.md")
		must(t, err)
		return len(vs) > 0
	}
	for deadline := time.Now().Add(10 * time.Second); n.Store.Has(unused) || kept(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the loop kept content nothing needs, or a version over the vault's limit")
		}
	}
	for _, line := range []string{"INFO removed unused content", "INFO removed old versions"} {
		for deadline := time.Now().Add(5 * time.Second); count(rec.messages(), line) != 1; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the loop didn't log %q once:\n%s", line, strings.Join(rec.messages(), "\n"))
			}
		}
	}
}

// agedStore makes everything in n's store older than gcGrace.
func agedStore(t *testing.T, n *Node) {
	t.Helper()
	old := time.Now().Add(-2 * gcGrace)
	must(t, filepath.Walk(filepath.Join(n.Store.Root, "objects"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return os.Chtimes(p, old, old)
	}))
}
