//go:build !linux && !darwin

package fsmeta

import "io/fs"

func sysStat(fs.FileInfo) (ino uint64, ctime int64) { return 0, 0 }
