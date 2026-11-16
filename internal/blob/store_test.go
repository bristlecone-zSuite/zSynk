package blob

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

func TestPutGet(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Put([]byte("hello synk"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Has(h) {
		t.Fatal("missing")
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("hello synk")) {
		t.Fatalf("%q", got)
	}
	missing := strings.Repeat("ab", 32)
	if _, err := s.Get(missing); err == nil {
		t.Fatal("expected missing")
	}
}

func TestCorruptDetected(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Put([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.pathFor(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(h)
	if err == nil {
		t.Fatal("expected corrupt")
	}
}

func TestValidateHash(t *testing.T) {
	good := strings.Repeat("0123456789abcdef", 4)
	if err := ValidateHash(good); err != nil {
		t.Fatalf("%s: %v", good, err)
	}
	for _, bad := range []string{
		"",
		good[:63],
		good + "0",
		strings.ToUpper(good),
		" " + good[1:],
		good[:62] + "/x",
		"../" + good[3:],
		strings.Repeat("g", 64),
	} {
		if err := ValidateHash(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Has(strings.ToUpper(good)) {
		t.Fatal("Has accepted an uppercase hash")
	}
}

func TestDedup(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, err := s.Put([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Put([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("same bytes must share hash")
	}
}

func TestPutReader(t *testing.T) {
	s := newStore(t)
	content := bytes.Repeat([]byte("synk"), 100_000)
	h, n, err := s.PutReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if h != atomicfile.HashBytes(content) || n != int64(len(content)) {
		t.Fatalf("PutReader returned %s, %d", h, n)
	}
	if got, err := s.Get(h); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("stored %d bytes: %v", len(got), err)
	}
	if again, _, err := s.PutReader(bytes.NewReader(content)); err != nil || again != h {
		t.Fatalf("storing it again: %s, %v", again, err)
	}

	failing := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errors.New("gone")))
	if _, _, err := s.PutReader(failing); err == nil {
		t.Fatal("PutReader stored content from a reader that failed")
	}
	if s.Has(atomicfile.HashBytes([]byte("partial"))) {
		t.Fatal("what a failed reader held was stored")
	}
	if left, _ := os.ReadDir(s.tmpDir()); len(left) != 0 {
		t.Fatalf("PutReader left %d temporary files", len(left))
	}
}

func TestPutFile(t *testing.T) {
	s := newStore(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.md")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h, n, err := s.PutFile(a)
	if err != nil || h != atomicfile.HashBytes([]byte("hello")) || n != 5 || !s.Has(h) {
		t.Fatalf("PutFile: %s, %d, %v", h, n, err)
	}
	if os.Geteuid() == 0 {
		return
	}
	// With nowhere to put a copy, only content the store has can be put.
	if err := os.Chmod(s.tmpDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(s.tmpDir(), 0o700)
	if got, _, err := s.PutFile(b); err != nil || got != h {
		t.Fatalf("putting content the store has copied it again: %v", err)
	}
}

func TestNewDiscardsUnfinishedContent(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(s.tmpDir(), "put-1")
	if err := os.WriteFile(left, []byte("cut off"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); err == nil {
		t.Fatal("content a stopped process was storing is still there")
	}
}

// Verify stops at the first read past size, rather than reading a source
// that holds more to its end.
func TestVerifyStopsPastSize(t *testing.T) {
	tooFar := errors.New("read past size")
	src := io.MultiReader(bytes.NewReader(make([]byte, 1<<20)), iotest.ErrReader(tooFar))
	if _, err := io.ReadAll(Verify(src, atomicfile.HashBytes(nil), 10)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("reading a source that holds more than size: %v", err)
	}
}

// Sweep removes only blobs keep rejects and leftovers of interrupted
// writes, and only once they're older than the cutoff.
func TestSweep(t *testing.T) {
	s := newStore(t)
	aged := func(p string) {
		t.Helper()
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	put := func(content string) (string, string) {
		t.Helper()
		h, err := s.Put([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.pathFor(h)
		if err != nil {
			t.Fatal(err)
		}
		return h, p
	}
	kept, keptPath := put("kept")
	unused, unusedPath := put("unused")
	recent, _ := put("recent")
	aged(keptPath)
	aged(unusedPath)
	dir := filepath.Dir(unusedPath)
	leftover, other := filepath.Join(dir, atomicfile.TempPrefix+"1"), filepath.Join(dir, "notes.txt")
	for _, p := range []string{leftover, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		aged(p)
	}

	removed, freed, err := s.Sweep(func(h string) bool { return h == kept }, time.Now().Add(-time.Hour))
	if err != nil || removed != 2 || freed != int64(len("unused")+1) {
		t.Fatalf("Sweep removed %d files, %d bytes: %v", removed, freed, err)
	}
	if !s.Has(kept) || !s.Has(recent) || s.Has(unused) {
		t.Fatalf("after Sweep: kept %v, recent %v, unused %v", s.Has(kept), s.Has(recent), s.Has(unused))
	}
	if _, err := os.Stat(leftover); err == nil {
		t.Fatal("Sweep left an interrupted write")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("Sweep removed a file it doesn't know: %v", err)
	}
}

func TestRemove(t *testing.T) {
	s := newStore(t)
	h, err := s.Put([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(h); err != nil || s.Has(h) {
		t.Fatalf("Remove: %v", err)
	}
	if err := s.Remove(h); err != nil {
		t.Fatalf("removing a blob that isn't stored: %v", err)
	}
	if err := s.Remove("../" + h[3:]); err == nil {
		t.Fatal("Remove took a path")
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
