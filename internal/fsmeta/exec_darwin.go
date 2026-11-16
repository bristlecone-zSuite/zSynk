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
	name := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return execFS(string(name))
}

// execFS reports whether the file system with the given type name keeps
// executable bits.
func execFS(name string) bool {
	switch name {
	case "apfs", "hfs", "zfs", "nfs":
		return true
	}
	return false
}
