package blob

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

// A transfer that stops part way leaves what arrived staged, and the next
// one continues from there instead of starting over.
func TestResumeStagesAndFinishes(t *testing.T) {
	s := newStore(t)
	content := bytes.Repeat([]byte("synk resume "), 40_000)
	h := atomicfile.HashBytes(content)

	if n := s.PartialSize(h); n != 0 {
		t.Fatalf("PartialSize of content never staged: %d", n)
	}

	// First transfer delivers half and is cut off.
	p, err := s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	half := len(content) / 2
	if _, err := p.Write(content[:half]); err != nil {
		t.Fatal(err)
	}
	if got := p.Size(); got != int64(half) {
		t.Fatalf("Size after half: %d", got)
	}
	// Close without Finish keeps what arrived.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.PartialSize(h); got != int64(half) {
		t.Fatalf("PartialSize after the transfer stopped: %d", got)
	}

	// The next transfer continues from there and finishes.
	p, err = s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Size(); got != int64(half) {
		t.Fatalf("Resume didn't continue from where the last stopped: %d", got)
	}
	if _, err := p.Write(content[half:]); err != nil {
		t.Fatal(err)
	}
	if err := p.Finish(); err != nil {
		t.Fatal(err)
	}
	if !s.Has(h) {
		t.Fatal("Finish didn't store the blob")
	}
	if got, err := s.Get(h); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("Finish stored %d bytes: %v", len(got), err)
	}
	if got := s.PartialSize(h); got != 0 {
		t.Fatalf("the staged content is still there after Finish: %d", got)
	}

	// Content the store already has needs no staging.
	p, err = s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Finish(); err != nil {
		t.Fatalf("finishing content the store already has: %v", err)
	}
	if got := s.PartialSize(h); got != 0 {
		t.Fatalf("staged content left behind: %d", got)
	}
}

// A partial whose content doesn't hash to its name is discarded, never
// stored, and the next transfer starts over.
func TestResumeFinishVerifiesHash(t *testing.T) {
	s := newStore(t)
	h := atomicfile.HashBytes([]byte("the real content"))

	p, err := s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("garbage")); err != nil {
		t.Fatal(err)
	}
	if err := p.Finish(); err == nil {
		t.Fatal("Finish stored content that doesn't hash to its name")
	}
	if s.Has(h) {
		t.Fatal("the wrong content was stored")
	}
	if got := s.PartialSize(h); got != 0 {
		t.Fatalf("a partial that failed to verify is kept: %d bytes", got)
	}
}

// Discard removes the staged content.
func TestResumeDiscard(t *testing.T) {
	s := newStore(t)
	h := atomicfile.HashBytes([]byte("discard me"))

	p, err := s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("half")); err != nil {
		t.Fatal(err)
	}
	if err := p.Discard(); err != nil {
		t.Fatal(err)
	}
	if got := s.PartialSize(h); got != 0 {
		t.Fatalf("Discard left %d bytes", got)
	}
	if err := p.Finish(); err == nil {
		t.Fatal("Finish after Discard stored nothing into the store")
	}
}

// Only one transfer may append to a hash's staged content at a time.
func TestResumeBusy(t *testing.T) {
	s := newStore(t)
	h := atomicfile.HashBytes([]byte("one at a time"))

	p, err := s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resume(h); err == nil {
		t.Fatal("a second Resume while one is open")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Size(); got != 5 {
		t.Fatalf("Resume after Close: %d bytes", got)
	}
	if err := p.Discard(); err != nil {
		t.Fatal(err)
	}
}

// The sweep removes staged content old enough, and keeps what a transfer
// is still working on.
func TestSweepRemovesStalePartials(t *testing.T) {
	s := newStore(t)
	old := atomicfile.HashBytes([]byte("stale partial"))
	fresh := atomicfile.HashBytes([]byte("fresh partial"))

	p, err := s.Resume(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = s.Resume(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	aged := time.Now().Add(-3 * time.Hour)
	oldPath := filepath.Join(s.Root, "partial", old)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Chtimes(oldPath, aged, aged))

	removed, freed, err := s.Sweep(func(string) bool { return false }, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || freed != 3 {
		t.Fatalf("Sweep removed %d files, %d bytes", removed, freed)
	}
	if got := s.PartialSize(old); got != 0 {
		t.Fatal("the stale partial survived the sweep")
	}
	if got := s.PartialSize(fresh); got != 3 {
		t.Fatal("the sweep took a partial a transfer may still continue")
	}
}

// Opening the store keeps staged content, unlike what a stopped process
// was writing into tmp.
func TestNewKeepsPartials(t *testing.T) {
	root := t.TempDir()
	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := atomicfile.HashBytes([]byte("kept across restarts"))
	p, err := s.Resume(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("kept")); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root, nil); err != nil {
		t.Fatal(err)
	}
	s2, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.PartialSize(h); got != 4 {
		t.Fatalf("New() discarded staged content: %d bytes left", got)
	}
}

// Resume and PartialSize accept only real hashes.
func TestResumeValidatesHash(t *testing.T) {
	s := newStore(t)
	if _, err := s.Resume("nothex"); err == nil {
		t.Fatal("Resume took a bad hash")
	}
	if s.PartialSize("nothex") != 0 {
		t.Fatal("PartialSize took a bad hash")
	}
}
