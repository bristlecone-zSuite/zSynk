package fsmeta

import (
	"io/fs"
	"syscall"
)

func sysStat(fi fs.FileInfo) (ino uint64, ctime int64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return st.Ino, st.Ctim.Nano()
}
