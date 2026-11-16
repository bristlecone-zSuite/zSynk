//go:build !darwin

package scan

import "io/fs"

func datalessFlag(fs.FileInfo) bool { return false }
