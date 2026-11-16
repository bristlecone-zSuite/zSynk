package safepath

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWithin(t *testing.T) {
	cases := []struct {
		p, dir string
		want   bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b/", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/a/b/../c", "/a/b", false},
		{"/x", "/", true},
	}
	for _, c := range cases {
		if got := Within(c.p, c.dir); got != c.want {
			t.Errorf("Within(%q, %q) = %v", c.p, c.dir, got)
		}
	}
	if runtime.GOOS == "darwin" && !Within("/Users/me/Library/Mobile Documents/x", "/Users/me/library/mobile documents") {
		t.Error("case matters on a case-insensitive volume")
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got := Resolve(filepath.Join(dir, "link", "not", "yet")); got != filepath.Join(want, "not", "yet") {
		t.Fatalf("Resolve = %s", got)
	}
}
