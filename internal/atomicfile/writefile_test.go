package atomicfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFile covers writing a new file, overwriting an existing one,
// the default permission when perm is 0, and an explicit permission.
func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")

	if err := WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte("hello")) {
		t.Fatalf("file holds %q; want %q", b, "hello")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm = %o; want 600", got)
	}

	// Overwriting keeps only the new contents.
	if err := WriteFile(path, []byte("goodbye"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err = os.ReadFile(path); err != nil || string(b) != "goodbye" {
		t.Fatalf("after overwrite: %q, %v", b, err)
	}

	// perm 0 means 0600.
	zero := filepath.Join(dir, "zero.md")
	if err := WriteFile(zero, []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if st, err = os.Stat(zero); err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Fatalf("perm 0 gave %o; want 600", got)
	}

	// An explicit permission is honoured.
	shared := filepath.Join(dir, "shared.md")
	if err := WriteFile(shared, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, err = os.Stat(shared); err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Fatalf("perm = %o; want 644", got)
	}

	// No temp files are left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), TempPrefix) {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}

	// Writing into a missing directory creates it.
	nested := filepath.Join(dir, "a", "b", "c.md")
	if err := WriteFile(nested, []byte("deep"), 0o600); err != nil {
		t.Fatalf("WriteFile into a missing directory: %v", err)
	}
	if b, err := os.ReadFile(nested); err != nil || string(b) != "deep" {
		t.Fatalf("nested file: %q, %v", b, err)
	}
}

// TestHashReader checks the hex digest and byte count it returns.
func TestHashReader(t *testing.T) {
	data := []byte("hello")
	want := HashBytes(data)
	got, n, err := HashReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("HashReader = %q; want %q", got, want)
	}
	if n != int64(len(data)) {
		t.Fatalf("n = %d; want %d", n, len(data))
	}
	// Empty input has a known digest and count.
	empty := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got, n, err = HashReader(strings.NewReader("")); err != nil || got != empty || n != 0 {
		t.Fatalf("HashReader(empty) = %q, %d, %v; want %q, 0, nil", got, n, err, empty)
	}
	// An error from the reader comes back.
	if _, _, err = HashReader(errReader{}); err == nil {
		t.Fatal("HashReader on a failing reader returned no error")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, os.ErrInvalid }

// TestSyncDir just checks it doesn't blow up on a real and a missing dir.
func TestSyncDir(t *testing.T) {
	syncDir(t.TempDir())
	syncDir(filepath.Join(t.TempDir(), "missing"))
}

// TestDiskAvailable checks it returns a plausible free-space figure for a
// real directory and an error for a missing one.
func TestDiskAvailable(t *testing.T) {
	free, err := DiskAvailable(t.TempDir())
	if err != nil {
		t.Fatalf("DiskAvailable: %v", err)
	}
	if free == 0 {
		t.Fatal("DiskAvailable reported 0 free bytes")
	}
	if _, err := DiskAvailable(filepath.Join(t.TempDir(), "missing", "file")); err == nil {
		t.Fatal("DiskAvailable on a missing path returned no error")
	}
}
