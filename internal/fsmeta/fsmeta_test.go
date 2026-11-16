package fsmeta

import (
	"io/fs"
	"testing"
)

func TestExec(t *testing.T) {
	for mode, want := range map[fs.FileMode]bool{
		0o755: true,
		0o700: true,
		0o100: true,
		0o644: false,
		0o600: false,
		0o011: false,
	} {
		if Exec(mode) != want {
			t.Errorf("Exec(%o) = %v", mode, !want)
		}
	}
}
