package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
)

// The pause, resume, remove, and unpair commands change what Synk does
// with a library and a device, and keep the files.
func TestMembershipCommands(t *testing.T) {
	data := t.TempDir()
	t.Setenv("SYNK_DIR", data)
	dir := filepath.Join(t.TempDir(), "notes")
	tg, err := connect("Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := tg.node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	tg.close()

	out, err := output(t, func() error { return cmdPause([]string{"--library", "notes"}) })
	if err != nil || !strings.Contains(out, "Paused notes") {
		t.Fatalf("synk pause printed (%v):\n%s", err, out)
	}
	if paused := libraryPaused(t, data, "notes"); !paused {
		t.Fatal("the library isn't paused in the config")
	}

	out, err = output(t, func() error { return cmdResume([]string{"--library", "notes"}) })
	if err != nil || !strings.Contains(out, "Resumed notes") {
		t.Fatalf("synk resume printed (%v):\n%s", err, out)
	}
	if libraryPaused(t, data, "notes") {
		t.Fatal("the library is still paused in the config")
	}

	out, err = output(t, func() error { return cmdRemove([]string{"--library", "notes"}) })
	if err != nil || !strings.Contains(out, "Removed notes") {
		t.Fatalf("synk remove printed (%v):\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.md")); err != nil {
		t.Fatalf("removing the library touched its files: %v", err)
	}
	n, err := engine.Open(data, "Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Libraries()) != 0 {
		t.Fatal("the removed library is still configured")
	}
	n.Close()

	// An unknown library is an error with the command's name on it.
	if _, err := output(t, func() error { return cmdPause([]string{"--library", "nope"}) }); err == nil || !strings.Contains(err.Error(), "unknown library") {
		t.Fatalf("synk pause for an unknown library: %v", err)
	}
	if _, err := output(t, func() error { return cmdUnpair([]string{"--device", "QQKX-AHEH-BZYC-WDA9"}) }); err == nil || !strings.Contains(err.Error(), "unknown device") {
		t.Fatalf("synk unpair for an unpaired device: %v", err)
	}
	if _, err := output(t, func() error { return cmdPause(nil) }); err == nil || !strings.Contains(err.Error(), "--library is required") {
		t.Fatalf("synk pause without a library: %v", err)
	}
}

func libraryPaused(t *testing.T, data, id string) bool {
	t.Helper()
	cfg, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.LibraryByID(id)
	if l == nil {
		t.Fatalf("the %s library is gone from the config", id)
	}
	return l.Paused
}
