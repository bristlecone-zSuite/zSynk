// Package fsmeta extracts the file attributes Synk uses to notice that a file
// changed without reading it, and the ones it syncs besides content.
package fsmeta

import "io/fs"

// Stat is what changes when a file's content may have changed. Inode and
// ctime catch a replace-by-rename that keeps the size and mtime.
type Stat struct {
	Size  int64       `json:"size"`
	MTime int64       `json:"mtime"`
	CTime int64       `json:"ctime,omitempty"`
	Ino   uint64      `json:"ino,omitempty"`
	Mode  fs.FileMode `json:"mode"`
}

func FromInfo(fi fs.FileInfo) Stat {
	s := Stat{Size: fi.Size(), MTime: fi.ModTime().UnixNano(), Mode: fi.Mode()}
	s.Ino, s.CTime = sysStat(fi)
	return s
}

// Exec reports whether mode makes a file executable. As in Git, the
// owner's bit decides.
func Exec(mode fs.FileMode) bool { return mode&0o100 != 0 }
