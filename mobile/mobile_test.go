package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T, name string) *Synk {
	t.Helper()
	s, err := Open(t.TempDir(), name)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// bridge decodes the envelope the string-returning methods use. On
// failure it returns the envelope's error text, so assertions read the
// way a caller sees a failed call.
func bridge(t *testing.T, envelope string) string {
	t.Helper()
	var e struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal([]byte(envelope), &e); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, envelope)
	}
	if !e.OK {
		return "error: " + e.Error
	}
	return string(e.Data)
}

type statusJSON struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Peers    []struct {
		DeviceID string `json:"deviceId"`
		Name     string `json:"name"`
	} `json:"peers"`
}

func statusOf(t *testing.T, s *Synk) statusJSON {
	t.Helper()
	var st statusJSON
	if err := json.Unmarshal([]byte(bridge(t, s.Status())), &st); err != nil {
		t.Fatalf("Status payload is not JSON: %v", err)
	}
	return st
}

func TestOpenStatusFingerprintClose(t *testing.T) {
	s, err := Open(t.TempDir(), "iPhone")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	st := statusOf(t, s)
	if st.Name != "iPhone" {
		t.Errorf("Status name = %q, want %q", st.Name, "iPhone")
	}
	if st.DeviceID == "" {
		t.Error("Status has no device ID")
	}
	if fp := s.Fingerprint(); fp == "" {
		t.Error("Fingerprint is empty")
	}
}

func TestAddLibraryListsAndDefaults(t *testing.T) {
	s := openTest(t, "iPhone")
	path := filepath.Join(t.TempDir(), "vault")
	lib := bridge(t, s.AddLibrary("obsidian", "", path))
	var got struct {
		ID     string `json:"id"`
		Preset string `json:"preset"`
		Label  string `json:"label"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal([]byte(lib), &got); err != nil {
		t.Fatalf("AddLibrary returned bad JSON: %v\n%s", err, lib)
	}
	if got.ID != "obsidian" {
		t.Errorf("ID = %q, want the preset's %q", got.ID, "obsidian")
	}
	if got.Label != "Obsidian" {
		t.Errorf("Label = %q, want the preset's name", got.Label)
	}
	if got.Path != path {
		t.Errorf("Path = %q, want %q", got.Path, path)
	}
	list := bridge(t, s.Libraries())
	if !strings.Contains(list, `"id":"obsidian"`) {
		t.Errorf("Libraries = %s, want it to list the added library", list)
	}
}

func TestAddLibraryUnknownPresetFails(t *testing.T) {
	s := openTest(t, "iPhone")
	out := s.AddLibrary("does-not-exist", "", filepath.Join(t.TempDir(), "x"))
	var e struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("envelope is not JSON: %v\n%s", err, out)
	}
	if e.OK {
		t.Errorf("AddLibrary with an unknown preset succeeded: %s", out)
	}
	if !strings.Contains(e.Error, "unknown preset") {
		t.Errorf("error = %q, want it to name the unknown preset", e.Error)
	}
}

func TestPauseResumeRemoveRoundTrip(t *testing.T) {
	s := openTest(t, "iPhone")
	path := filepath.Join(t.TempDir(), "vault")
	if out := s.AddLibrary("obsidian", "", path); strings.HasPrefix(bridge(t, out), "error: ") {
		t.Fatalf("AddLibrary: %v", out)
	}
	if err := s.PauseLibrary("obsidian"); err != nil {
		t.Fatalf("PauseLibrary: %v", err)
	}
	if list := bridge(t, s.Libraries()); !strings.Contains(list, `"paused":true`) {
		t.Errorf("Libraries after pause = %s; want paused true", list)
	}
	if err := s.ResumeLibrary("obsidian"); err != nil {
		t.Fatalf("ResumeLibrary: %v", err)
	}
	if list := bridge(t, s.Libraries()); strings.Contains(list, `"paused":true`) {
		t.Errorf("Libraries after resume = %s; want no paused library", list)
	}
	if err := s.RemoveLibrary("obsidian"); err != nil {
		t.Fatalf("RemoveLibrary: %v", err)
	}
	if list := bridge(t, s.Libraries()); list != "[]" {
		t.Errorf("Libraries after remove = %s; want []", list)
	}
}

func TestPairWithMacRejectsBadLink(t *testing.T) {
	s := openTest(t, "iPhone")
	if err := s.PairWithMac("https://example.com/not-synk"); err == nil {
		t.Error("PairWithMac accepted a non-synk link, want an error")
	}
	if err := s.PairWithMac("synk://pair?code=x"); err == nil {
		t.Error("PairWithMac accepted a link with no token or fingerprint, want an error")
	}
}

// TestPairThenSyncThroughMobile pairs two embedded nodes the way the app
// will — a scanned link on one side, StartPairing on the other — and then
// syncs a file both ways through the same API the app drives.
func TestPairThenSyncThroughMobile(t *testing.T) {
	a := openTest(t, "Mac")
	b := openTest(t, "iPhone")

	var offer struct {
		DeviceID string `json:"deviceId"`
		QR       string `json:"qr"`
	}
	if err := json.Unmarshal([]byte(bridge(t, a.StartPairing())), &offer); err != nil {
		t.Fatalf("StartPairing payload is not JSON: %v", err)
	}
	if offer.QR == "" {
		t.Fatal("StartPairing returned no QR link")
	}
	if err := b.PairWithMac(offer.QR); err != nil {
		t.Fatalf("PairWithMac: %v", err)
	}

	// Each side must now list the other as a peer.
	bst := statusOf(t, b)
	if len(bst.Peers) != 1 || bst.Peers[0].DeviceID != offer.DeviceID {
		t.Errorf("iPhone peers = %+v, want exactly %q", bst.Peers, offer.DeviceID)
	}
	ast := statusOf(t, a)
	if len(ast.Peers) != 1 || ast.Peers[0].DeviceID != bst.DeviceID {
		t.Errorf("Mac peers = %+v, want exactly %q", ast.Peers, bst.DeviceID)
	}

	// A library on each side, in temporary folders only.
	aPath := filepath.Join(t.TempDir(), "vault")
	bPath := filepath.Join(t.TempDir(), "vault")
	if out := a.AddLibrary("obsidian", "", aPath); strings.HasPrefix(bridge(t, out), "error: ") {
		t.Fatalf("AddLibrary on Mac: %v", out)
	}
	if out := b.AddLibrary("obsidian", "", bPath); strings.HasPrefix(bridge(t, out), "error: ") {
		t.Fatalf("AddLibrary on iPhone: %v", out)
	}

	// Mac writes, iPhone pulls with its own sync call.
	if err := writeFile(filepath.Join(aPath, "note.md"), "# hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := b.n.SyncAllPeers(); err != nil {
		t.Fatalf("SyncAllPeers on iPhone: %v", err)
	}
	if got, err := readFile(filepath.Join(bPath, "note.md")); err != nil || got != "# hello\n" {
		t.Errorf("iPhone has %q, err %v; want %q", got, err, "# hello\n")
	}

	// iPhone writes, Mac pulls.
	if err := writeFile(filepath.Join(bPath, "reply.md"), "from the phone\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.n.SyncAllPeers(); err != nil {
		t.Fatalf("SyncAllPeers on Mac: %v", err)
	}
	if got, err := readFile(filepath.Join(aPath, "reply.md")); err != nil || got != "from the phone\n" {
		t.Errorf("Mac has %q, err %v; want %q", got, err, "from the phone\n")
	}
}
