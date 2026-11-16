package fsmeta

import (
	"path/filepath"
	"testing"
)

func TestExecFS(t *testing.T) {
	for magic, want := range map[uint32]bool{
		0xEF53:     true,  // ext4
		0x58465342: true,  // xfs
		0x9123683E: true,  // btrfs
		0x01021994: true,  // tmpfs
		0x2FC12FC1: true,  // zfs
		0xF2F52010: true,  // f2fs
		0xCA451A4E: true,  // bcachefs
		0x794C7630: true,  // overlay
		0x6969:     true,  // nfs
		0x4D44:     false, // vfat
		0x2011BAB0: false, // exfat
		0x5346544E: false, // ntfs3
		0xFF534D42: false, // cifs
		0xFE534D42: false, // smb2
		0x65735546: false, // fuse, which ntfs-3g uses
	} {
		if execFS(magic) != want {
			t.Errorf("execFS(%#x) = %v", magic, !want)
		}
	}
}

func TestExecBits(t *testing.T) {
	dir := t.TempDir()
	if !ExecBits(dir) {
		t.Skipf("%s is on a file system Synk doesn't know", dir)
	}
	if ExecBits(filepath.Join(dir, "missing")) {
		t.Error("a missing path keeps executable bits")
	}
	if ExecBits("/proc") {
		t.Error("procfs keeps executable bits")
	}
}
