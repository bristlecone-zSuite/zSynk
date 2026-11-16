package engine

import (
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"strings"
	"unicode"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
)

var (
	// ErrNotPaired means the other end of a connection isn't a paired
	// device.
	ErrNotPaired = errors.New("engine: not a paired device")
	// ErrPairingRejected means pairing failed because of the offer: the
	// code or token was wrong or used up, the offer expired, or the device
	// that answered doesn't hold the offer's key.
	ErrPairingRejected = errors.New("engine: pairing rejected")
	// ErrNotShared means a library doesn't sync with the device.
	ErrNotShared = errors.New("engine: library not shared with this device")
	// ErrPaused means a library is paused, so it doesn't sync.
	ErrPaused = errors.New("engine: library is paused")
	// ErrRefused means the other device turned down a sync: it doesn't
	// know this device, or doesn't sync the library with it now.
	ErrRefused = errors.New("engine: the other device refused")
)

// peerByKey returns the paired device that holds pub. Device IDs are a
// short hash of the key, so the whole key is compared.
func (n *Node) peerByKey(pub ed25519.PublicKey) (config.Peer, bool) {
	p, ok := n.Peer(identity.IDFromPublic(pub))
	if !ok {
		return config.Peer{}, false
	}
	stored, err := identity.ParsePublic(p.PublicKey)
	if err != nil || !stored.Equal(pub) {
		return config.Peer{}, false
	}
	return p, true
}

// serverTLS accepts any device that proves it holds its key. What an
// unpaired device may do is up to the session: only pair.
func (n *Node) serverTLS() *tls.Config {
	return transport.Config(n.cert, func(ed25519.PublicKey) error { return nil })
}

// anyPeer accepts only paired devices.
func (n *Node) anyPeer(pub ed25519.PublicKey) error {
	if _, ok := n.peerByKey(pub); !ok {
		return ErrNotPaired
	}
	return nil
}

// thisPeer accepts only the device p.
func thisPeer(p config.Peer) func(ed25519.PublicKey) error {
	return func(pub ed25519.PublicKey) error {
		want, err := identity.ParsePublic(p.PublicKey)
		if err != nil || !want.Equal(pub) {
			return ErrNotPaired
		}
		return nil
	}
}

// maxNameLen bounds the device names peers send, which people read.
const maxNameLen = 64

// cleanName makes a name a peer sent safe to show: printable, on one line,
// and not too long.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxNameLen {
		s = string(r[:maxNameLen])
	}
	if s == "" {
		return "Unnamed device"
	}
	return s
}
