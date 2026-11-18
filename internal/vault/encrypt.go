// At-rest encryption for the vault: turn it on and off, and answer
// readers in the clear without leaving plaintext behind. See
// docs/decisions/0016-vault-at-rest-encryption-is-per-file-aead.md.
package vault

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

// IsEncrypted says whether the vault at root has been encrypted.
func IsEncrypted(root string) bool {
	return exists(filepath.Join(root, keycheckName))
}

// KeyForPassphrase derives the vault key the keycheck seals for. It fails
// with ErrVaultLocked if the passphrase is wrong, and errors if the vault
// holds no keycheck.
func KeyForPassphrase(root, passphrase string) ([]byte, error) {
	return keycheckKey(root, passphrase)
}

// EnableEncryption seals every byte in the vault with a key from
// passphrase, and leaves a keycheck that later opens must match. Versions,
// package contents, metadata and quarantined bytes all go under the same
// key. Snapshots that were still copying when this ran stay plain on disk,
// and are sealed by their own writer when it finishes.
func (v *Vault) EnableEncryption(passphrase string) error {
	if strings.TrimSpace(passphrase) == "" {
		return errors.New("vault: the passphrase is empty")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if IsEncrypted(v.Root) {
		return errors.New("vault: already encrypted; decrypt it first")
	}
	if err := writeKeycheck(v.Root, passphrase); err != nil {
		return err
	}
	key, err := keycheckKey(v.Root, passphrase)
	if err != nil {
		return err
	}
	v.key = key
	if err := sealAll(filepath.Join(v.Root, "versions"), key); err != nil {
		return err
	}
	if err := sealAll(filepath.Join(v.Root, "quarantine"), key); err != nil {
		return err
	}
	syncDir(v.Root)
	return nil
}

// DisableEncryption unseals everything and takes the keycheck down, so the
// vault is plaintext again. It refuses a passphrase that doesn't open the
// keycheck, before touching a byte.
func (v *Vault) DisableEncryption(passphrase string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	key, err := keycheckKey(v.Root, passphrase)
	if err != nil {
		return err
	}
	if err := unsealAll(filepath.Join(v.Root, "versions"), key); err != nil {
		return err
	}
	if err := unsealAll(filepath.Join(v.Root, "quarantine"), key); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(v.Root, keycheckName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	v.key = nil
	syncDir(v.Root)
	return nil
}

// sealAll seals every plain regular file under root, in place. Snapshot
// work directories below root are skipped: their writer seals them when it
// finishes. Root itself is never skipped, so this also seals a work
// directory passed as root.
func sealAll(root string, key []byte) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			if p != root && d.IsDir() && strings.HasPrefix(d.Name(), tmpPrefix) {
				return fs.SkipDir
			}
			return nil
		}
		if isSealedPath(p) {
			return nil
		}
		return ignoreSealErr(key, p)
	})
}

// unsealAll is sealAll backwards.
func unsealAll(root string, key []byte) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			if d.IsDir() && strings.HasPrefix(d.Name(), tmpPrefix) {
				return fs.SkipDir
			}
			return nil
		}
		if !isSealedPath(p) {
			return nil
		}
		return ignoreUnsealErr(key, p)
	})
}

// ignoreSealErr seals p, reporting real failures and treating a file that
// went away while walking as done.
func ignoreSealErr(key []byte, p string) error {
	err := sealPath(key, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ignoreUnsealErr is ignoreSealErr for unsealing.
func ignoreUnsealErr(key []byte, p string) error {
	err := unsealFile(key, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// sealPath seals the plain file at p, replacing it. The plaintext is read
// whole; snapshot content is copied before this runs, so nothing user-
// owned reaches here larger than a file the machine already held.
func sealPath(key []byte, p string) error {
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	sealed, err := sealBytes(key, raw)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(p, sealed, 0o600)
}

// unsealFile unseals the sealed file at p, replacing it.
func unsealFile(key []byte, p string) error {
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	plain, err := openBytes(key, raw)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(p, plain, 0o600)
}

// sealDir seals a snapshot temp in place: its content and metadata. It
// runs while the vault lock is held, just before the version is renamed
// into place.
func (v *Vault) sealDir(dir string) error {
	if v.key == nil {
		return nil
	}
	if err := sealAll(dir, v.key); err != nil {
		return err
	}
	p := filepath.Join(dir, "meta.json")
	if isSealedPath(p) {
		return nil
	}
	return ignoreSealErr(v.key, p)
}

// decryptContent lays the plaintext of content into dst, whether content
// is a sealed file or a tree of sealed files.
func decryptContent(key []byte, content, dst string) error {
	info, err := os.Stat(content)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !isSealedPath(content) {
			raw, err := os.ReadFile(content)
			if err != nil {
				return err
			}
			return atomicfile.WriteFile(dst, raw, 0o600)
		}
		return openFileTo(key, content, dst)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(content, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(content, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if !isSealedPath(p) {
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return atomicfile.WriteFile(target, raw, 0o600)
		}
		return openFileTo(key, p, target)
	})
}

// plainSizeOf reports the size the content would have in the clear: what
// the reader gets, whether the vault is sealed or not.
func plainSizeOf(p string) (int64, error) {
	info, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if n, ok := sealedPlainLength(p); ok {
			return n, nil
		}
		return info.Size(), nil
	}
	var n int64
	err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if want, ok := sealedPlainLength(path); ok {
			n += want
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	})
	return n, err
}

// sealedPlainLength reads the plaintext length from a sealed envelope's
// header without opening it, and says whether the file is one.
func sealedPlainLength(p string) (int64, bool) {
	f, err := os.Open(p)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	head := make([]byte, len(envelopeMagic)+32+8)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, false
	}
	if string(head[:len(envelopeMagic)]) != envelopeMagic {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(head[len(envelopeMagic)+32:])), true
}
