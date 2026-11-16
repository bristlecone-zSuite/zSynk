//go:build unix

package scan

import (
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestScanSkipsSpecialFiles(t *testing.T) {
	root := library(t)
	must(t, syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	done := make(chan *Result, 1)
	go func() {
		res, _ := run(t, root, Options{})
		done <- res
	}()
	select {
	case res := <-done:
		if len(res.Items) != 0 || !slices.Contains(res.Unconfirmed, "pipe") {
			t.Fatalf("items %+v, unconfirmed %v", res.Items, res.Unconfirmed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan blocked reading a FIFO")
	}
}
