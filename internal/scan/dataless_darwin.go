//go:build darwin

package scan

import (
	"io/fs"
	"syscall"
)

// sfDataless is SF_DATALESS from <sys/stat.h>. The file's content, or a
// folder's listing, is in the cloud, and reading it would download it.
const sfDataless = 0x40000000

func datalessFlag(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&sfDataless != 0
}
