package watch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFingerprintChanges(t *testing.T) {
	dir := t.TempDir()
	a, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("expected fingerprint to change")
	}
}

func TestFingerprintSeesWhatTheScannerSyncs(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	before, err := Fingerprint(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".synk/staging/x", ".SYNK/y"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Adding .synk changes the folder's own time, but a file system with
	// coarse timestamps may not show that yet.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(dir, later, later); err != nil {
		t.Fatal(err)
	}
	if after, _ := Fingerprint(link); after != before {
		t.Fatal("changes inside .synk changed the fingerprint")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after, _ := Fingerprint(link); after == before {
		t.Fatal("a change the scanner syncs, under a symlinked root, didn't change the fingerprint")
	}
	if runtime.GOOS == "windows" {
		return
	}
	before, _ = Fingerprint(link)
	if err := os.Chmod(filepath.Join(dir, ".git", "HEAD"), 0o700); err != nil {
		t.Fatal(err)
	}
	if after, _ := Fingerprint(link); after == before {
		t.Fatal("making a file executable didn't change the fingerprint")
	}
}

func TestRunNotifies(t *testing.T) {
	dir := t.TempDir()
	w := New(30 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 1)
	go w.Run(ctx, func() []string { return []string{dir} }, func(root string) {
		select {
		case got <- root:
		default:
		}
	})
	time.Sleep(60 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case root := <-got:
		if root != dir {
			t.Fatalf("%s", root)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for watch event")
	}
}

// A change made while onChange runs, as a sync does, is reported too.
func TestRunNotifiesChangesDuringOnChange(t *testing.T) {
	dir := t.TempDir()
	w := New(30 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan int, 8)
	n := 0
	go w.Run(ctx, func() []string { return []string{dir} }, func(string) {
		n++
		if n == 1 {
			if err := os.WriteFile(filepath.Join(dir, "during.md"), []byte("x"), 0o600); err != nil {
				t.Error(err)
			}
		}
		select {
		case calls <- n:
		default:
		}
	})
	time.Sleep(60 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("call %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no call %d: a change made during onChange wasn't reported", want)
		}
	}
}
