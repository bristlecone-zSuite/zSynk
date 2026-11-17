package mobile

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/api"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
)

// TestMobileSyncsWithTheDaemonCore pairs the phone-side bridge with a
// node opened the way the daemon opens it — engine.Open serving peers,
// pairing armed through api.StartPairing — and syncs files both ways.
// The phone never runs a daemon process, but it must speak the same
// protocol as the Mac that does: this is the end-to-end iPhone↔Mac
// proof CI can reach, bridge on one side and daemon core on the other.
func TestMobileSyncsWithTheDaemonCore(t *testing.T) {
	// The Mac side, exactly as `synk serve` and the bundled daemon run it.
	mac, err := engine.Open(t.TempDir(), "Mac")
	if err != nil {
		t.Fatalf("engine.Open: %v", err)
	}
	t.Cleanup(func() { _ = mac.Close() })
	ln, err := mac.ServePeers("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ServePeers: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go mac.WatchAndSync(ctx)

	// The phone side: the bridge the app drives.
	phone := openTest(t, "iPhone")

	offer, err := api.StartPairing(mac)
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	if err := phone.PairWithMac(offer.QR); err != nil {
		t.Fatalf("PairWithMac: %v", err)
	}

	// Each side must list the other.
	pst := statusOf(t, phone)
	if len(pst.Peers) != 1 || pst.Peers[0].DeviceID != mac.Device.DeviceID {
		t.Errorf("phone peers = %+v, want exactly %q", pst.Peers, mac.Device.DeviceID)
	}
	var mst statusJSON
	if b, err := json.Marshal(mac.Status()); err != nil {
		t.Fatalf("Mac status: %v", err)
	} else if err := json.Unmarshal(b, &mst); err != nil {
		t.Fatalf("Mac status is not the served shape: %v", err)
	}
	if len(mst.Peers) != 1 || mst.Peers[0].DeviceID != pst.DeviceID {
		t.Errorf("Mac peers = %+v, want exactly %q", mst.Peers, pst.DeviceID)
	}

	// A library on each side, in temporary folders only. The Mac enables
	// its library the way the API handler does; the phone through the
	// bridge, with the path inside its sandbox.
	macPath := filepath.Join(t.TempDir(), "vault")
	if err := mac.EnableLibrary(config.Library{ID: "obsidian", Preset: "obsidian", Label: "Obsidian", Path: macPath}); err != nil {
		t.Fatalf("EnableLibrary on Mac: %v", err)
	}
	phonePath := filepath.Join(t.TempDir(), "vault")
	if out := phone.AddLibrary("obsidian", "", phonePath); hasBridgeError(t, out) {
		t.Fatalf("AddLibrary on phone: %v", out)
	}

	// Mac writes, phone pulls through its own sync call.
	if err := writeFile(filepath.Join(macPath, "note.md"), "# from the Mac\n"); err != nil {
		t.Fatal(err)
	}
	if err := phone.SyncNow(); err != nil {
		t.Fatalf("SyncNow on phone: %v", err)
	}
	if got, err := readFile(filepath.Join(phonePath, "note.md")); err != nil || got != "# from the Mac\n" {
		t.Errorf("phone has %q, err %v; want %q", got, err, "# from the Mac\n")
	}

	// Phone writes, Mac pulls through its watch loop.
	if err := writeFile(filepath.Join(phonePath, "reply.md"), "from the phone\n"); err != nil {
		t.Fatal(err)
	}
	if err := mac.SyncAllPeers(); err != nil {
		t.Fatalf("SyncAllPeers on Mac: %v", err)
	}
	if got, err := readFile(filepath.Join(macPath, "reply.md")); err != nil || got != "from the phone\n" {
		t.Errorf("Mac has %q, err %v; want %q", got, err, "from the phone\n")
	}
}

// hasBridgeError reports whether an envelope answers with an error, the
// way callers of the string-returning methods see failure.
func hasBridgeError(t *testing.T, envelope string) bool {
	t.Helper()
	var e struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(envelope), &e); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, envelope)
	}
	return !e.OK
}
