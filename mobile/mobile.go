// Package mobile exposes the Synk engine over the narrow, bindable API
// that gomobile needs to build the iOS framework (docs/XCODE.md,
// "gomobile"). Values that would not bind — structs, slices of structs,
// channels — cross the boundary as JSON strings with the same shapes the
// REST API serves, so the app-side clients decode them with the types
// they already have.
//
// A Synk opened here is the whole node: it serves peer connections on an
// ephemeral port and runs the watch-and-sync loop until Close, the way
// `synk serve` does for a daemon. On iOS there is no separate daemon
// process, so the embedding host owns the lifecycle through Open/Close.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/bristlecone-zSuite/zSynk/internal/api"
	"github.com/bristlecone-zSuite/zSynk/internal/apps"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
)

// Synk is an opened, running node. Methods are safe to call from any
// thread; the engine serializes its own state.
type Synk struct {
	n      *engine.Node
	cancel context.CancelFunc
	ln     net.Listener
}

// Open opens (or creates) the node in dataDir, starts serving peer
// connections on an ephemeral port, and starts the watch-and-sync loop.
// The pairing link and Status carry the actual port, so callers never
// need to pick one.
func Open(dataDir, deviceName string) (*Synk, error) {
	n, err := engine.Open(dataDir, deviceName)
	if err != nil {
		return nil, err
	}
	ln, err := n.ServePeers("0.0.0.0:0")
	if err != nil {
		_ = n.Close()
		return nil, fmt.Errorf("mobile: serving peers: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go n.WatchAndSync(ctx)
	return &Synk{n: n, cancel: cancel, ln: ln}, nil
}

// Close stops the watch-and-sync loop, stops serving peers, and releases
// the data folder. The Synk must not be used afterwards.
func (s *Synk) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	return s.n.Close()
}

// Fingerprint is this device's key fingerprint, as shown to compare
// during pairing.
func (s *Synk) Fingerprint() string {
	return s.n.Fingerprint()
}

// Status returns the same JSON as GET /v1/status: the device, its
// libraries with their sync state, and its peers.
//
// Methods whose result is a string return a JSON envelope instead of a
// Go error: gomobile bridges a non-null string return and an out error
// pointer in a way that cannot express "nil on error" safely, while
// boolean results bridge to throws. The envelope is
// {"ok":true,"data":…} or {"ok":false,"error":"…"}.
func (s *Synk) Status() string {
	b, err := json.Marshal(s.n.Status())
	return envelope(b, err)
}

// Libraries returns the configured libraries as JSON, the same array as
// GET /v1/libraries, in the envelope form Status uses.
func (s *Synk) Libraries() string {
	b, err := json.Marshal(s.n.Libraries())
	return envelope(b, err)
}

// AddLibrary enables a library the way POST /v1/libraries does: the ID
// defaults to the preset's, so the same app on two devices syncs between
// them, and an empty path falls back to the preset's default location.
// One library per preset is the iOS case; pass preset, label, and path.
// It returns the stored library as JSON in the envelope form.
func (s *Synk) AddLibrary(preset, label, path string) string {
	l := config.Library{ID: preset, Preset: preset, Label: label, Path: path}
	if l.Path == "" {
		p, err := apps.DefaultPath(preset)
		if err != nil {
			return envelope(nil, err)
		}
		l.Path = p
	}
	if err := s.n.EnableLibrary(l); err != nil {
		return envelope(nil, err)
	}
	lib, ok := s.n.Library(l.ID)
	if !ok {
		return envelope(nil, errors.New("mobile: the library vanished as it was added"))
	}
	b, err := json.Marshal(lib)
	return envelope(b, err)
}

// PauseLibrary stops syncing a library without touching its files.
func (s *Synk) PauseLibrary(id string) error {
	return s.n.PauseLibrary(id)
}

// ResumeLibrary resumes syncing a paused library.
func (s *Synk) ResumeLibrary(id string) error {
	return s.n.ResumeLibrary(id)
}

// RemoveLibrary forgets the library and keeps its files, versions, and
// index (decision 0005).
func (s *Synk) RemoveLibrary(id string) error {
	return s.n.RemoveLibrary(id)
}

// StartPairing arms a pairing offer and returns it as JSON in the
// envelope form: the code to read out, the synk://pair link to show as a
// QR code, and the device identity the joiner can pin. The offer expires
// if nobody joins.
func (s *Synk) StartPairing() string {
	offer, err := api.StartPairing(s.n)
	if err != nil {
		return envelope(nil, err)
	}
	b, err := json.Marshal(offer)
	return envelope(b, err)
}

// envelope wraps data — or err, if it is not nil — as the JSON envelope
// the string-returning methods hand across the bridge: success carries
// the payload under "data", failure the message under "error".
func envelope(data []byte, err error) string {
	if err != nil {
		e := struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}{false, err.Error()}
		b, _ := json.Marshal(e)
		return string(b)
	}
	w := struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}{true, data}
	b, _ := json.Marshal(w)
	return string(b)
}

// PairWithMac pairs with the device that made the offer behind a scanned
// synk://pair link. Every address in the link is tried until one works;
// the link's fingerprint pins the answer to the offering device's key.
func (s *Synk) PairWithMac(link string) error {
	l, err := pairing.ParseURI(link)
	if err != nil {
		return err
	}
	if len(l.Addrs) == 0 {
		return errors.New("mobile: the pairing link has no addresses")
	}
	var lastErr error
	for _, addr := range l.Addrs {
		if err := s.n.JoinPeer(addr, l.Code, l.Token, l.Fingerprint); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}
