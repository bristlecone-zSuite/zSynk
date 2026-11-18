package engine

import (
	"errors"
	"path/filepath"

	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

// vaultRoot is where the node keeps its vault.
func vaultRoot(dataDir string) string {
	return filepath.Join(dataDir, "vault")
}

// EnableVaultEncryption seals everything the node keeps on disk — the
// vault's snapshots and quarantines, and every blob — under a key
// derived from the passphrase. Library folders themselves are the
// user's working copies and stay as they are; this protects Synk's own
// store. The call holds the node lock and blocks until every blob is
// re-sealed, so it should run while nothing else is going on.
func (n *Node) EnableVaultEncryption(passphrase string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if vault.IsEncrypted(vaultRoot(n.DataDir)) {
		return errors.New("vault is already encrypted")
	}
	if passphrase == "" {
		return errors.New("a passphrase is required")
	}
	if err := n.Vault.EnableEncryption(passphrase); err != nil {
		return err
	}
	// EnableEncryption wrote the keycheck; derive the working key from
	// it and move the node onto it.
	key, err := vault.KeyForPassphrase(vaultRoot(n.DataDir), passphrase)
	if err != nil {
		return err
	}
	nv, err := vault.Open(vaultRoot(n.DataDir), key)
	if err != nil {
		return err
	}
	n.Vault = nv
	se := vault.NewCipher(key)
	n.Store.SetSealer(se)
	if _, err := n.Store.SealAtRest(); err != nil {
		return err
	}
	return nil
}

// DisableVaultEncryption unseals everything under the passphrase's key
// and returns the node to a plain, at-rest-unprotected store.
func (n *Node) DisableVaultEncryption(passphrase string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !vault.IsEncrypted(vaultRoot(n.DataDir)) {
		return errors.New("vault is not encrypted")
	}
	if err := n.Vault.DisableEncryption(passphrase); err != nil {
		return err
	}
	if _, err := n.Store.UnsealAtRest(); err != nil {
		return err
	}
	nv, err := vault.Open(vaultRoot(n.DataDir), nil)
	if err != nil {
		return err
	}
	n.Vault = nv
	n.Store.SetSealer(nil)
	return nil
}
