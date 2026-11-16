//go:build !linux && !darwin

package fsmeta

// ExecBits reports whether the file system holding path keeps executable
// bits. Synk only trusts the ones it knows on Linux and macOS.
func ExecBits(string) bool { return false }
