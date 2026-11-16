package apps

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/packages"
)

func TestObsidianDefaultIsNotICloud(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	icloud := filepath.Join(home, "Library", "Mobile Documents", "iCloud~md~obsidian", "Documents")
	local := filepath.Join(home, "Documents", "Obsidian")
	for _, d := range []string{icloud, local} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	for _, f := range Discover() {
		if f.Preset.ID != "obsidian" {
			continue
		}
		if !slices.Equal(f.Paths, []string{local}) || !slices.Equal(f.CloudPaths, []string{icloud}) {
			t.Fatalf("paths %v, cloud paths %v", f.Paths, f.CloudPaths)
		}
		return
	}
	t.Fatal("no obsidian preset")
}

func TestDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	icloud := filepath.Join(home, "Library", "Mobile Documents", "iCloud~md~obsidian", "Documents")
	if err := os.MkdirAll(icloud, 0o700); err != nil {
		t.Fatal(err)
	}
	if p, err := DefaultPath("obsidian"); !errors.Is(err, ErrNoFolder) || !strings.Contains(err.Error(), icloud) {
		t.Fatalf("a vault only in iCloud must be refused, saying where it is: %q, %v", p, err)
	}
	local := filepath.Join(home, "Obsidian")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if p, err := DefaultPath("obsidian"); err != nil || p != local {
		t.Fatalf("obsidian: %q, %v", p, err)
	}
	managed, err := ManagedPath("pages")
	if err != nil {
		t.Fatal(err)
	}
	if p, err := DefaultPath("pages"); err != nil || p != managed {
		t.Fatalf("pages: %q, %v; want %q", p, err, managed)
	}
	if p, err := DefaultPath("custom"); !errors.Is(err, ErrNoFolder) {
		t.Fatalf("custom: %q, %v", p, err)
	}
	if _, err := DefaultPath("nope"); err == nil {
		t.Fatal("unknown preset")
	}
}

func TestInCloud(t *testing.T) {
	home := t.TempDir()
	drive := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	if err := os.MkdirAll(drive, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "Drive")
	if err := os.Symlink(drive, link); err != nil {
		t.Skip(err)
	}
	for p, want := range map[string]bool{
		drive:                        true,
		filepath.Join(link, "Notes"): true,
		filepath.Join(home, "Library", "CloudStorage", "GoogleDrive-me", "Notes"): true,
		filepath.Join(home, "Documents", "Notes"):                                 false,
		filepath.Join(home, "Library", "Containers", "pro.writer.mac"):            false,
	} {
		if got := InCloud(home, p); got != want {
			t.Errorf("InCloud(%s) = %v", p, got)
		}
	}
}

func TestCatalogIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Catalog() {
		if p.ID == "" || p.Name == "" {
			t.Fatalf("empty preset %+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("duplicate %s", p.ID)
		}
		seen[p.ID] = true
	}
	if _, ok := ByID("obsidian"); !ok {
		t.Fatal("obsidian")
	}
	if _, ok := ByID("nope"); ok {
		t.Fatal("unknown")
	}
}

// Every package format a preset lists syncs as a package.
func TestPresetsListOnlyPackages(t *testing.T) {
	for _, p := range Catalog() {
		for _, ext := range p.PackageExts {
			if !packages.IsPackageName("Document" + ext) {
				t.Errorf("%s lists %s, which doesn't sync as a package", p.ID, ext)
			}
		}
	}
}

func TestExpandHome(t *testing.T) {
	got := expand("/home/x", "~/Library/Foo")
	if got != "/home/x/Library/Foo" {
		t.Fatal(got)
	}
}
