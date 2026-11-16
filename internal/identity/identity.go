package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

const fileName = "identity.json"

// Device is the local device keypair and human name.
type Device struct {
	Name       string    `json:"name"`
	DeviceID   string    `json:"deviceId"`
	PublicKey  string    `json:"publicKey"`  // base32, no padding
	PrivateKey string    `json:"privateKey"` // base32, no padding; never leaves the data folder
	CreatedAt  time.Time `json:"createdAt"`
}

type fileV1 struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	DeviceID   string    `json:"deviceId"`
	PublicKey  string    `json:"publicKey"`
	PrivateKey string    `json:"privateKey"`
	CreatedAt  time.Time `json:"createdAt"`
}

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// IDFromPublic returns a stable, typable device ID from an Ed25519 public key.
func IDFromPublic(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	enc := crockford.EncodeToString(sum[:20])
	if len(enc) < 16 {
		enc = crockford.EncodeToString(sum[:])
	}
	enc = strings.ToUpper(enc)
	parts := make([]string, 0, 4)
	for i := 0; i+4 <= 16 && i < len(enc); i += 4 {
		parts = append(parts, enc[i:i+4])
	}
	return strings.Join(parts, "-")
}

func Generate(name string) (*Device, ed25519.PrivateKey, error) {
	if strings.TrimSpace(name) == "" {
		name = "Unnamed device"
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	d := &Device{
		Name:       name,
		DeviceID:   IDFromPublic(pub),
		PublicKey:  encodeKey(pub),
		PrivateKey: encodeKey(priv),
		CreatedAt:  time.Now().UTC(),
	}
	return d, priv, nil
}

func LoadOrCreate(dataDir, name string) (*Device, ed25519.PrivateKey, error) {
	path := filepath.Join(dataDir, fileName)
	b, err := os.ReadFile(path)
	if err == nil {
		return parse(b)
	}
	if !os.IsNotExist(err) {
		return nil, nil, err
	}
	d, priv, err := Generate(name)
	if err != nil {
		return nil, nil, err
	}
	if err := Save(dataDir, d); err != nil {
		return nil, nil, err
	}
	return d, priv, nil
}

func Save(dataDir string, d *Device) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	payload := fileV1{
		Version:    1,
		Name:       d.Name,
		DeviceID:   d.DeviceID,
		PublicKey:  d.PublicKey,
		PrivateKey: d.PrivateKey,
		CreatedAt:  d.CreatedAt,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dataDir, fileName), raw, 0o600)
}

func parse(b []byte) (*Device, ed25519.PrivateKey, error) {
	var f fileV1
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, nil, err
	}
	if f.Version != 1 {
		return nil, nil, fmt.Errorf("identity: unsupported version %d", f.Version)
	}
	privRaw, err := decodeKey(f.PrivateKey)
	if err != nil {
		return nil, nil, err
	}
	if len(privRaw) != ed25519.PrivateKeySize {
		return nil, nil, fmt.Errorf("identity: bad private key size")
	}
	priv := ed25519.PrivateKey(privRaw)
	pub := priv.Public().(ed25519.PublicKey)
	id := IDFromPublic(pub)
	if f.DeviceID != "" && f.DeviceID != id {
		return nil, nil, fmt.Errorf("identity: device id mismatch (file may be corrupted)")
	}
	d := &Device{
		Name:       f.Name,
		DeviceID:   id,
		PublicKey:  encodeKey(pub),
		PrivateKey: f.PrivateKey,
		CreatedAt:  f.CreatedAt,
	}
	return d, priv, nil
}

func (d *Device) Private() (ed25519.PrivateKey, error) {
	raw, err := decodeKey(d.PrivateKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity: bad private key size")
	}
	return ed25519.PrivateKey(raw), nil
}

func (d *Device) Public() (ed25519.PublicKey, error) {
	raw, err := decodeKey(d.PublicKey)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: bad public key size")
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePublic decodes a stored public key (base32, no padding).
func ParsePublic(s string) (ed25519.PublicKey, error) {
	raw, err := decodeKey(s)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: bad public key size")
	}
	return ed25519.PublicKey(raw), nil
}

// EncodePublic encodes a public key the way ParsePublic reads it.
func EncodePublic(pub ed25519.PublicKey) string {
	return encodeKey(pub)
}

// Fingerprint is the SHA-256 of a public key in hex. An offer carries it so
// the joining device accepts only the key that made the offer.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// ParseFingerprint reads a fingerprint as a person might paste it: in
// either case, with spaces, colons, or dashes between digits.
func ParseFingerprint(s string) (string, error) {
	fp := strings.Map(func(r rune) rune {
		switch r {
		case ' ', ':', '-':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(s)))
	if b, err := hex.DecodeString(fp); err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("identity: a fingerprint is %d hex digits", 2*sha256.Size)
	}
	return fp, nil
}

func encodeKey(b []byte) string {
	return strings.TrimRight(base32Std(b), "=")
}

func decodeKey(s string) ([]byte, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	return base32.StdEncoding.DecodeString(s)
}

func base32Std(b []byte) string {
	return strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}
