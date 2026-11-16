package engine

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
)

// Device IDs are a short hash of a key, so a paired device is known by its
// whole key, not by an ID that matches.
func TestPairedDevicesAreKnownByTheirWholeKey(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	pub := public(t, b)
	other, _, err := ed25519.GenerateKey(nil)
	must(t, err)
	must(t, a.addPeer(config.Peer{DeviceID: b.Device.DeviceID, Name: "iPhone", PublicKey: identity.EncodePublic(other)}))
	p, _ := a.Peer(b.Device.DeviceID)
	if _, ok := a.peerByKey(pub); ok {
		t.Fatal("a key that only shares the device ID counts as paired")
	}
	if !errors.Is(a.anyPeer(pub), ErrNotPaired) || !errors.Is(thisPeer(p)(pub), ErrNotPaired) {
		t.Fatal("a key that only shares the device ID passes the handshake")
	}
	if thisPeer(p)(other) != nil {
		t.Fatal("the key on record fails the handshake")
	}
	if thisPeer(config.Peer{PublicKey: "not a key"})(other) == nil {
		t.Fatal("a device with an unreadable key passes the handshake")
	}
}

// Text from another device can't move the cursor or fill a screen.
func TestPeerText(t *testing.T) {
	for in, want := range map[string]string{
		"library paused":               "library paused",
		"two\nlines\x1b[2J and\ta tab": "two lines [2J and a tab",
		strings.Repeat("é", 300):       strings.Repeat("é", maxPeerText) + "…",
	} {
		if got := peerText(in); got != want {
			t.Errorf("peerText(%q) = %q; want %q", in, got, want)
		}
	}
}
