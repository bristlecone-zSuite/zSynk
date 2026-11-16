package fsmeta

import "syscall"

// ExecBits reports whether the file system holding path keeps executable
// bits. FAT, exFAT, NTFS and SMB shares don't: they show every file as
// executable, or none, depending on how they're mounted.
func ExecBits(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	return execFS(uint32(st.Type))
}

// execFS reports whether the file system with the given statfs magic
// number keeps executable bits.
func execFS(magic uint32) bool {
	switch magic {
	case 0xEF53, // ext2, ext3, ext4
		0x58465342, // xfs
		0x9123683E, // btrfs
		0x01021994, // tmpfs
		0x2FC12FC1, // zfs
		0xF2F52010, // f2fs
		0xCA451A4E, // bcachefs
		0x794C7630, // overlay
		0x6969:     // nfs
		return true
	}
	return false
}
