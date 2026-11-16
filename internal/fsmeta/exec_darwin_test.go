package fsmeta

import (
	"path/filepath"
	"testing"
)

func TestExecFS(t *testing.T) {
	for name, want := range map[string]bool{
		"apfs":    true,
		"hfs":     true,
		"zfs":     true,
		"nfs":     true,
		"msdos":   false,
		"exfat":   false,
		"ntfs":    false,
		"smbfs":   false,
		"webdav":  false,
		"macfuse": false,
		"":        false,
	} {
		if execFS(name) != want {
			t.Errorf("execFS(%q) = %v", name, !want)
		}
	}
}

func TestExecBits(t *testing.T) {
	dir := t.TempDir()
	if !ExecBits(dir) {
		t.Errorf("%s, on the system volume, doesn't keep executable bits", dir)
	}
	if ExecBits(filepath.Join(dir, "missing")) {
		t.Error("a missing path keeps executable bits")
	}
	if ExecBits("/dev") {
		t.Error("devfs keeps executable bits")
	}
}
