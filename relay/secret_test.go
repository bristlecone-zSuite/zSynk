package relay_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/relay"
)

// The relay secret derives from the two peers' public keys (decision 0012),
// so both devices compute the same secret without any extra exchange, and
// it changes when either device re-keys.
func TestSecretFromKeys(t *testing.T) {
	keys := make([]ed25519.PublicKey, 3)
	for i := range keys {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = pub
	}
	a, b, c := keys[0], keys[1], keys[2]

	if relay.SecretFromKeys(a, b) != relay.SecretFromKeys(b, a) {
		t.Fatal("the secret depends on argument order")
	}
	if len(relay.SecretFromKeys(a, b)) != 64 {
		t.Fatalf("secret %q is not 64 hex characters", relay.SecretFromKeys(a, b))
	}
	if relay.SecretFromKeys(a, b) == relay.SecretFromKeys(a, c) {
		t.Fatal("two pairs share a secret")
	}

	// The documented formula: SHA-256 over the lexicographically smaller
	// raw key, a zero separator, the larger key.
	lo, hi := a, b
	if string(lo) > string(hi) {
		lo, hi = hi, lo
	}
	h := sha256.New()
	h.Write(lo)
	h.Write([]byte{0})
	h.Write(hi)
	if got := relay.SecretFromKeys(a, b); got != hex.EncodeToString(h.Sum(nil)) {
		t.Fatalf("secret %q does not match the formula in decision 0012", got)
	}
}
