package engine

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

// openVault opens the node's vault. A vault that has been encrypted needs
// its key: the environment supplies the passphrase (SYNK_VAULT_PASSPHRASE)
// and the vault's keycheck proves it, before anything is read. An
// encrypted vault with no passphrase fails loudly rather than serving
// nothing quietly.
func openVault(dataDir string) (*vault.Vault, []byte, error) {
	root := filepath.Join(dataDir, "vault")
	var key []byte
	if pass := strings.TrimSpace(os.Getenv("SYNK_VAULT_PASSPHRASE")); pass != "" {
		k, err := vault.KeyForPassphrase(root, pass)
		if err != nil {
			return nil, nil, err
		}
		key = k
	}
	v, err := vault.Open(root, key)
	if err != nil {
		return nil, nil, err
	}
	return v, key, nil
}
