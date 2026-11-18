package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"golang.org/x/crypto/argon2"
)

// At-rest encryption. A version's content, its meta.json, and quarantined
// bytes are stored as AEAD envelopes so a stolen disk or backup does not
// reveal kept versions. The key comes from a passphrase through Argon2id;
// Synk holds no copy and there is no recovery without the passphrase.
//
// Every sealed file starts with envelopeMagic and carries a random 32-byte
// base, the plaintext length, and AES-256-GCM chunks of chunkBytes. The
// chunk nonce is the first 12 bytes of SHA-256(base || big-endian chunk
// index), so one key never reuses a nonce across files, chunks, or writes.
// Because the magic is per file, a migration that is interrupted halfway
// leaves the vault readable: sealed files open, plain files still do.

const (
	// keyBytes is the AES-256 key size.
	keyBytes = 32
	// saltBytes is the Argon2id salt size.
	saltBytes = 16
	// chunkBytes is the plaintext size of one envelope chunk: 1 MiB, so a
	// multi-gigabyte version never needs a multi-gigabyte buffer.
	chunkBytes = 1 << 20
	// overheadBytes is what sealing adds: magic, per-file base, length,
	// and the AEAD tag of one chunk.
	overheadBytes = len(envelopeMagic) + 32 + 8 + 16
	// envelopeMagic opens every sealed file.
	envelopeMagic = "SYNKVAULT1\x00"

	// Argon2id parameters follow the OWASP baseline for interactive use:
	// 64 MiB of memory, three passes. On a phone or a busy Mac this is a
	// fraction of a second, and unlocking happens once per start.
	argonTime    = 3
	argonMemory  = 64 * 1024
	keycheckName = "keycheck.json"
)

// ErrVaultLocked means the vault is encrypted and no usable key was given.
var ErrVaultLocked = errors.New("vault: encrypted; a passphrase is needed (SYNK_VAULT_PASSPHRASE)")

// hexOf renders a sha256 sum lowercase.
func hexOf(sum [sha256.Size]byte) string {
	return hex.EncodeToString(sum[:])
}

// deriveKey turns a passphrase and a salt into an AES-256 key. Same
// passphrase and salt, same key; anything else, a different key.
func deriveKey(pass string, salt []byte) []byte {
	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}
	if threads > 4 {
		threads = 4
	}
	return argon2.IDKey([]byte(pass), salt, argonTime, argonMemory, uint8(threads), keyBytes)
}

// chunkNonce derives the nonce of one chunk from the file's random base.
func chunkNonce(base []byte, index uint64) []byte {
	h := sha256.New()
	h.Write(base)
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], index)
	h.Write(idx[:])
	return h.Sum(nil)[:12]
}

// isSealed reports whether raw starts with the envelope magic.
func isSealed(raw []byte) bool {
	return len(raw) >= len(envelopeMagic) && string(raw[:len(envelopeMagic)]) == envelopeMagic
}

// isSealedPath reports whether the file at path is sealed.
func isSealedPath(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [len(envelopeMagic)]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return string(magic[:]) == envelopeMagic
}

// newGCM builds the AEAD for a key.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealBytes seals one in-memory value: magic, a random base, the plaintext
// length, then the chunks. Used for meta.json, quarantine payloads, and
// the keycheck.
func sealBytes(key, plain []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	base := make([]byte, 32)
	if _, err := rand.Read(base); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(envelopeMagic)+len(base)+8+len(plain)+16)
	out = append(out, envelopeMagic...)
	out = append(out, base...)
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(plain)))
	out = append(out, length[:]...)
	for off, i := 0, uint64(0); off < len(plain) || (off == 0 && len(plain) == 0); off, i = off+chunkBytes, i+1 {
		end := off + chunkBytes
		if end > len(plain) {
			end = len(plain)
		}
		out = gcm.Seal(out, chunkNonce(base, i), plain[off:end], nil)
		if len(plain) == 0 {
			break
		}
	}
	return out, nil
}

// openBytes opens what sealBytes produced. Anything that fails the magic,
// the length, or authentication is refused.
func openBytes(key, raw []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	head := len(envelopeMagic) + 32 + 8
	if len(raw) < head+16 {
		return nil, errors.New("vault: not a sealed envelope")
	}
	if string(raw[:len(envelopeMagic)]) != envelopeMagic {
		return nil, errors.New("vault: not a sealed envelope")
	}
	base := raw[len(envelopeMagic) : len(envelopeMagic)+32]
	want := binary.BigEndian.Uint64(raw[head-8 : head])
	body := raw[head:]
	plain := make([]byte, 0, int(want))
	for off, i := 0, uint64(0); off < len(body); off, i = off+chunkBytes+16, i+1 {
		end := off + chunkBytes + 16
		if end > len(body) {
			end = len(body)
		}
		if end-off < 16 {
			return nil, errors.New("vault: truncated envelope chunk")
		}
		chunk, err := gcm.Open(nil, chunkNonce(base, i), body[off:end], nil)
		if err != nil {
			return nil, fmt.Errorf("vault: envelope chunk %d failed authentication", i)
		}
		plain = append(plain, chunk...)
	}
	if uint64(len(plain)) != want {
		return nil, fmt.Errorf("vault: envelope holds %d bytes, header says %d", len(plain), want)
	}
	return plain, nil
}

// sealFile seals a stream to dest, chunk by chunk, so large versions never
// sit whole in memory. It returns the plaintext size.
func sealFile(key []byte, src io.Reader, dest string) (int64, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return 0, err
	}
	base := make([]byte, 32)
	if _, err := rand.Read(base); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	var head [len(envelopeMagic) + 32 + 8]byte
	copy(head[:], envelopeMagic)
	copy(head[len(envelopeMagic):], base)
	if _, err := out.Write(head[:]); err != nil {
		return 0, err
	}
	chunk := make([]byte, chunkBytes)
	var size int64
	for i := uint64(0); ; i++ {
		n, rerr := io.ReadFull(src, chunk)
		if rerr == io.ErrUnexpectedEOF || rerr == io.EOF {
			if n == 0 && i > 0 {
				break // the last chunk was exactly one chunk; nothing more
			}
		} else if rerr != nil {
			return 0, rerr
		}
		if _, err := out.Write(gcm.Seal(nil, chunkNonce(base, i), chunk[:n], nil)); err != nil {
			return 0, err
		}
		size += int64(n)
		if rerr == io.ErrUnexpectedEOF || rerr == io.EOF {
			break
		}
		// A full chunk may still be the last one; the next read decides.
	}
	// The length lives after the base; the reader needs it before the
	// chunks, so write it in place now that it is known.
	if err := patchLength(out, base, size); err != nil {
		return 0, err
	}
	if err := out.Sync(); err != nil {
		return 0, err
	}
	return size, out.Close()
}

// patchLength writes the plaintext length into the header of an open
// sealed file (bytes after the magic and the base).
func patchLength(f *os.File, base []byte, size int64) error {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(size))
	off := int64(len(envelopeMagic) + 32)
	if _, err := f.WriteAt(length[:], off); err != nil {
		return err
	}
	return nil
}

// openFileTo decrypts the sealed file at src into dest. The caller owns
// dest and decides how long the plaintext stays around.
func openFileTo(key []byte, src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, len(envelopeMagic)+32+8)
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("vault: not a sealed envelope: %w", err)
	}
	if string(head[:len(envelopeMagic)]) != envelopeMagic {
		return errors.New("vault: not a sealed envelope")
	}
	base := head[len(envelopeMagic) : len(envelopeMagic)+32]
	want := binary.BigEndian.Uint64(head[len(head)-8:])
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	sealed := make([]byte, chunkBytes+16)
	var got uint64
	for i := uint64(0); ; i++ {
		n, rerr := io.ReadFull(f, sealed)
		if rerr == io.ErrUnexpectedEOF || rerr == io.EOF {
			// short final chunk, or nothing at all
		} else if rerr != nil {
			return rerr
		}
		if n == 0 {
			break
		}
		chunk, err := gcm.Open(nil, chunkNonce(base, i), sealed[:n], nil)
		if err != nil {
			return fmt.Errorf("vault: envelope chunk %d failed authentication", i)
		}
		if _, err := out.Write(chunk); err != nil {
			return err
		}
		got += uint64(len(chunk))
		if n < len(sealed) {
			break
		}
	}
	if got != want {
		return fmt.Errorf("vault: envelope holds %d bytes, header says %d", got, want)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// keycheckInfo is keycheck.json: how the passphrase becomes a key, and a
// value only the right key can open.
type keycheckInfo struct {
	Magic  string `json:"magic"`
	Salt   string `json:"salt"`
	Time   uint32 `json:"time"`
	Memory uint32 `json:"memoryKiB"`
	Check  string `json:"check"`
}

const keycheckMagic = "synk-vault-keycheck-1"
const keycheckPayload = "synk-vault-ok"

// writeKeycheck records salt and parameters and seals the check value with
// the key derived from passphrase. Writing it is what turns a vault into
// an encrypted one; from that moment a key is required.
func writeKeycheck(root, passphrase string) error {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key := deriveKey(passphrase, salt)
	sealed, err := sealBytes(key, []byte(keycheckPayload))
	if err != nil {
		return err
	}
	info := keycheckInfo{
		Magic:  keycheckMagic,
		Salt:   hex.EncodeToString(salt),
		Time:   argonTime,
		Memory: argonMemory,
		Check:  base64.StdEncoding.EncodeToString(sealed),
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(root, keycheckName), b, 0o600)
}

// keycheckKey derives the vault key from passphrase and the recorded salt,
// and refuses anything that cannot open the check value.
func keycheckKey(root, passphrase string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(root, keycheckName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("vault: not encrypted")
		}
		return nil, err
	}
	var info keycheckInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("vault: unreadable keycheck: %w", err)
	}
	if info.Magic != keycheckMagic {
		return nil, errors.New("vault: unreadable keycheck")
	}
	salt, err := hex.DecodeString(info.Salt)
	if err != nil || len(salt) != saltBytes {
		return nil, errors.New("vault: unreadable keycheck")
	}
	key := deriveKey(passphrase, salt)
	sealed, err := base64.StdEncoding.DecodeString(info.Check)
	if err != nil {
		return nil, errors.New("vault: unreadable keycheck")
	}
	payload, err := openBytes(key, sealed)
	if err != nil || string(payload) != keycheckPayload {
		return nil, fmt.Errorf("%w: the passphrase does not open this vault", ErrVaultLocked)
	}
	return key, nil
}

// vaultKeyFor answers: is this vault encrypted, and what key opens it?
// A vault with a keycheck needs the passphrase; without one it is plain
// and any key argument is ignored.
func vaultKeyFor(root string, key []byte) ([]byte, error) {
	present := exists(filepath.Join(root, keycheckName))
	if !present {
		return nil, nil // plain vault
	}
	if len(key) == 0 {
		return nil, ErrVaultLocked
	}
	// The caller's key must be the one the keycheck seals for.
	raw, err := os.ReadFile(filepath.Join(root, keycheckName))
	if err != nil {
		return nil, err
	}
	var info keycheckInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("vault: unreadable keycheck: %w", err)
	}
	salt, derr := hex.DecodeString(info.Salt)
	if derr != nil || len(salt) != saltBytes {
		return nil, errors.New("vault: unreadable keycheck")
	}
	sealed, berr := base64.StdEncoding.DecodeString(info.Check)
	if berr != nil {
		return nil, errors.New("vault: unreadable keycheck")
	}
	if _, err := openBytes(key, sealed); err != nil {
		return nil, fmt.Errorf("%w: the key does not open this vault", ErrVaultLocked)
	}
	return key, nil
}

// Sealer seals content at rest: the blob store's shape, defined here so
// the vault depends on nothing.
type Sealer interface {
	// SealReader writes r's plaintext to dest as a sealed envelope.
	SealReader(r io.Reader, dest string) (int64, error)
	// OpenSealed lays src's plaintext out at out.
	OpenSealed(src, out string) error
	// IsSealed says whether src holds a sealed envelope.
	IsSealed(src string) bool
}

// NewCipher adapts a vault key to the blob store's Sealer. A nil key
// yields a nil Sealer: the blob store keeps plaintext.
func NewCipher(key []byte) Sealer {
	if len(key) == 0 {
		return nil
	}
	return sealer{key: key}
}

// sealer is the Sealer the blob store uses when the vault is encrypted.
type sealer struct {
	key []byte
}

// SealReader writes r's plaintext to dest as a sealed envelope.
func (s sealer) SealReader(r io.Reader, dest string) (int64, error) {
	return sealFile(s.key, r, dest)
}

// OpenSealed lays src's plaintext out at out.
func (s sealer) OpenSealed(src, out string) error {
	return openFileTo(s.key, src, out)
}

// IsSealed says whether src holds a sealed envelope.
func (s sealer) IsSealed(src string) bool {
	return isSealedPath(src)
}
