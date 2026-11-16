package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "note.md")
	if err := WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	if err := WriteFile(path, []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Fatalf("got %q", got)
	}
}

func TestSameVolume(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if !SameVolume(dir, sub) {
		t.Fatal("a directory and its child reported different volumes")
	}
	if _, err := os.Stat("/proc/self"); err == nil && SameVolume(dir, "/proc") {
		t.Fatal("a temp directory and /proc reported the same volume")
	}
}

func TestHashBytes(t *testing.T) {
	if HashBytes([]byte{}) == "" {
		t.Fatal("empty hash")
	}
	if HashBytes([]byte("a")) == HashBytes([]byte("b")) {
		t.Fatal("hashes collided")
	}
}
