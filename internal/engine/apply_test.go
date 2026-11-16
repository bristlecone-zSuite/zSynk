package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/journal"
)

func pairedLibrary(t *testing.T) (a, b *Node, dirA, dirB string) {
	t.Helper()
	a = openNode(t, "A")
	b = openNode(t, "B")
	must(t, Pair(a, b))
	dirA = filepath.Join(t.TempDir(), "notes")
	dirB = filepath.Join(t.TempDir(), "notes")
	must(t, a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirA}))
	must(t, b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirB}))
	return a, b, dirA, dirB
}

func TestPackageAppliesAcrossVolumes(t *testing.T) {
	other := os.Getenv("SYNK_TEST_OTHER_VOLUME")
	if other == "" {
		other = "/dev/shm"
	}
	dirB, err := os.MkdirTemp(other, "synk-test-")
	if err != nil {
		t.Skipf("no second filesystem at %s; set SYNK_TEST_OTHER_VOLUME", other)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dirB) })
	b := openNode(t, "B")
	if atomicfile.SameVolume(dirB, b.DataDir) {
		t.Skipf("%s is on the same filesystem as the temp dir; set SYNK_TEST_OTHER_VOLUME", other)
	}
	a := openNode(t, "A")
	must(t, Pair(a, b))
	dirA := filepath.Join(t.TempDir(), "keynote")
	must(t, a.EnableLibrary(config.Library{ID: "keynote", Preset: "keynote", Label: "k", Path: dirA}))
	must(t, b.EnableLibrary(config.Library{ID: "keynote", Preset: "keynote", Label: "k", Path: dirB}))
	must(t, os.MkdirAll(filepath.Join(dirA, "Talk.key", "Data"), 0o700))
	must(t, os.WriteFile(filepath.Join(dirA, "Talk.key", "Index.zip"), []byte("idx"), 0o600))
	must(t, SyncOnce(a, b, "keynote"))
	got, err := os.ReadFile(filepath.Join(dirB, "Talk.key", "Index.zip"))
	if err != nil || string(got) != "idx" {
		t.Fatalf("package on another volume: %q, %v", got, err)
	}

	must(t, os.WriteFile(filepath.Join(dirA, "Talk.key", "Index.zip"), []byte("idx v2"), 0o600))
	must(t, SyncOnce(a, b, "keynote"))
	got, err = os.ReadFile(filepath.Join(dirB, "Talk.key", "Index.zip"))
	if err != nil || string(got) != "idx v2" {
		t.Fatalf("replacing a package on another volume: %q, %v", got, err)
	}
}

// A crash between the two renames of a package swap leaves the package in
// staging. If the library were scanned before recovery, the package would
// be recorded as deleted and the delete sent to every peer.
func TestRestartRecoversBeforeScanning(t *testing.T) {
	a := openNode(t, "A")
	dir := filepath.Join(t.TempDir(), "keynote")
	must(t, a.EnableLibrary(config.Library{ID: "keynote", Preset: "keynote", Label: "k", Path: dir}))
	must(t, os.MkdirAll(filepath.Join(dir, "Talk.key"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "Talk.key", "Index.zip"), []byte("v1"), 0o600))
	_, err := a.ScanLibrary("keynote")
	must(t, err)
	before := a.Index("keynote").Entries["Talk.key"]

	const tx = "tx-00000000000000c3"
	stage := filepath.Join(dir, ".synk", "staging", tx)
	must(t, os.MkdirAll(filepath.Join(stage, "new"), 0o700))
	must(t, os.WriteFile(filepath.Join(stage, "new", "Index.zip"), []byte("v2"), 0o600))
	must(t, os.Rename(filepath.Join(dir, "Talk.key"), filepath.Join(stage, "old")))
	for _, st := range []journal.Status{journal.StatusStarted, journal.StatusPrepared} {
		_, err := a.Journal.Append(journal.Record{ID: tx, Status: st, FolderID: "keynote", Op: "set", Path: "Talk.key", Staging: ".synk/staging/" + tx})
		must(t, err)
	}

	must(t, a.Close())
	restarted := openNodeAt(t, a.DataDir, "A")
	_, err = restarted.ScanLibrary("keynote")
	must(t, err)
	after := restarted.Index("keynote").Entries["Talk.key"]
	if after.Deleted || after.Hash != before.Hash {
		t.Fatalf("package after restart: %+v", after)
	}
	got, err := os.ReadFile(filepath.Join(dir, "Talk.key", "Index.zip"))
	if err != nil || string(got) != "v1" {
		t.Fatalf("package content after restart: %q, %v", got, err)
	}
}

func TestPullAppliesWhatItCanWhenContentIsMissing(t *testing.T) {
	a, b, dirA, dirB := pairedLibrary(t)
	must(t, os.WriteFile(filepath.Join(dirA, "x.md"), []byte("x"), 0o600))
	must(t, os.WriteFile(filepath.Join(dirA, "y.md"), []byte("y"), 0o600))
	ia, err := a.ExportIndex("notes")
	must(t, err)
	missing := ia.Entries["y.md"].Hash
	flaky := func(h string) ([]byte, error) {
		if h == missing {
			return nil, blob.ErrNotFound
		}
		return a.GetBlob(h)
	}

	err = b.PullFrom(ia, flaky)
	if err == nil || !strings.Contains(err.Error(), "y.md") {
		t.Fatalf("err = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dirB, "x.md")); err != nil || string(got) != "x" {
		t.Fatalf("x.md: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "y.md")); err == nil {
		t.Fatal("y.md applied without its content")
	}
	if _, ok := b.Index("notes").Entries["y.md"]; ok {
		t.Fatal("the index records y.md, so it would never be retried")
	}

	must(t, b.PullFrom(ia, a.GetBlob))
	if got, err := os.ReadFile(filepath.Join(dirB, "y.md")); err != nil || string(got) != "y" {
		t.Fatalf("y.md on retry: %q, %v", got, err)
	}
}
