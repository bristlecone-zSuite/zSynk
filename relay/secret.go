package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
)

// SecretFromKeys derives the pair's relay secret from the two peers' raw
// public keys, the formula in decision 0012 (docs/PROTOCOL.md "Relay"):
// hex(SHA-256(min(kA,kB) || 0x00 || max(kA,kB))) over the raw 32-byte
// Ed25519 keys. Both devices compute the same secret from the keys they
// already hold after pairing, and the secret changes when either device
// re-keys.
func SecretFromKeys(a, b ed25519.PublicKey) string {
	if bytes.Compare(a, b) > 0 {
		a, b = b, a
	}
	h := sha256.New()
	h.Write(a)
	h.Write([]byte{0})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
