package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
)

// needleIn reports whether any file under root contains needle. It is
// the whole point of the encryption tests: the bytes must not be
// readable anywhere in Synk's own store.
func needleIn(t testing.TB, root, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if bytes.Contains(raw, []byte(needle)) {
			found = true
		}
		return nil
	})
	return found
}

func TestVaultEncryptionRoundTrip(t *testing.T) {
	a := openNode(t, "alice")
	b := openNode(t, "bob")
	_ = Pair(a, b)
	dirA := filepath.Join(t.TempDir(), "n")
	dirB := filepath.Join(t.TempDir(), "n")
	_ = a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirA})
	_ = b.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dirB})
	secret := "quarterly numbers nobody may read from disk"
	if err := os.WriteFile(filepath.Join(dirA, "x.md"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncOnce(a, b, "notes"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []*Node{a, b} {
		if !needleIn(t, n.DataDir, secret) {
			t.Fatalf("sanity: %s should hold the plaintext before encryption", n.DataDir)
		}
	}

	if err := a.EnableVaultEncryption("correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := b.EnableVaultEncryption("correct horse"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []*Node{a, b} {
		if needleIn(t, n.DataDir, secret) {
			t.Fatalf("plaintext readable in %s after EnableVaultEncryption", n.DataDir)
		}
		if st := n.Status(); !st.VaultEncrypted {
			t.Fatal("Status must report vaultEncrypted")
		}
	}

	// Sync keeps working with sealed stores, and the new revision is
	// sealed on arrival too.
	v2 := "second revision, still secret"
	if err := os.WriteFile(filepath.Join(dirA, "x.md"), []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncOnce(a, b, "notes"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dirB, "x.md"))
	if err != nil || string(got) != v2 {
		t.Fatalf("sync after enable: %q (%v)", got, err)
	}
	for _, n := range []*Node{a, b} {
		if needleIn(t, n.DataDir, v2) {
			t.Fatalf("plaintext of the new revision readable in %s", n.DataDir)
		}
	}

	if err := a.EnableVaultEncryption("correct horse"); err == nil {
		t.Fatal("enabling twice must be refused")
	}

	// Restart without the passphrase: locked.
	datadir := b.DataDir
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(datadir, "bob"); err == nil {
		t.Fatal("a locked vault must refuse to open without the passphrase")
	}

	// With the right passphrase the node comes back whole.
	t.Setenv("SYNK_VAULT_PASSPHRASE", "correct horse")
	b2, err := Open(datadir, "bob")
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if st := b2.Status(); !st.VaultEncrypted {
		t.Fatal("reopened node must still report vaultEncrypted")
	}
	if err := SyncOnce(a, b2, "notes"); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(dirB, "x.md"))
	if err != nil || string(got) != v2 {
		t.Fatalf("sync after restart: %q (%v)", got, err)
	}

	// A wrong passphrase must not open the vault.
	if err := b2.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNK_VAULT_PASSPHRASE", "wrong horse")
	if _, err := Open(datadir, "bob"); err == nil {
		t.Fatal("the wrong passphrase must not open the vault")
	}

	// Turning it off puts the plaintext back.
	t.Setenv("SYNK_VAULT_PASSPHRASE", "correct horse")
	b3, err := Open(datadir, "bob")
	if err != nil {
		t.Fatal(err)
	}
	defer b3.Close()
	if err := b3.DisableVaultEncryption("correct horse"); err != nil {
		t.Fatal(err)
	}
	if st := b3.Status(); st.VaultEncrypted {
		t.Fatal("Status must clear vaultEncrypted after DisableVaultEncryption")
	}
	if !needleIn(t, b3.DataDir, secret) {
		t.Fatal("the old blob must be plaintext again after DisableVaultEncryption")
	}
}
