package fsmeta

import (
	"os"
	"path/filepath"
	"testing"
)

// groundTruthFolds asks the directory directly: it creates two names that
// differ only in case and reports whether they turned out to be one file.
// This is independent of FoldsCase, so the test checks the probe against
// reality instead of against itself.
func groundTruthFolds(t *testing.T, dir string) bool {
	t.Helper()
	a := filepath.Join(dir, "caseprobe-a")
	if err := os.WriteFile(a, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := filepath.Join(dir, "caseprobe-A")
	if err := os.WriteFile(b, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	// On a case-folding volume both writes landed in one file, so the file
	// now holds the second write's content.
	return string(first) == "b"
}

func TestFoldsCaseMatchesGroundTruth(t *testing.T) {
	dir := t.TempDir()
	want := groundTruthFolds(t, dir)
	if got := FoldsCase(dir); got != want {
		t.Fatalf("FoldsCase(%q) = %v, volume folds = %v", dir, got, want)
	}
}

// The probe must leave nothing behind, whatever the volume does.
func TestFoldsCaseLeavesNoProbeFiles(t *testing.T) {
	dir := t.TempDir()
	FoldsCase(dir)
	FoldsCase(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) >= 6 && e.Name()[:6] == ".synk-" {
			t.Fatalf("probe left %s behind", e.Name())
		}
	}
}
