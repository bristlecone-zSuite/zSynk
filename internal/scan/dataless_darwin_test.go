//go:build darwin

package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type statInfo struct {
	fs.FileInfo
	st *syscall.Stat_t
}

func (s statInfo) Sys() any { return s.st }

func TestDatalessFlag(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.md")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if datalessFlag(fi) {
		t.Fatal("a local file reported as dataless")
	}
	st := *fi.Sys().(*syscall.Stat_t)
	st.Flags |= sfDataless
	if !datalessFlag(statInfo{fi, &st}) {
		t.Fatal("SF_DATALESS not detected")
	}
}
