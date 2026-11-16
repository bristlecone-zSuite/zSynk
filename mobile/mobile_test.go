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
	b, err := s.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var st statusJSON
	if err := json.Unmarshal([]byte(b), &st); err != nil {
		t.Fatalf("Status is not JSON: %v\n%s", err, b)
	}
	return st
}

func TestOpenStatusFingerprintClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "iPhone")
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
	lib, err := s.AddLibrary("obsidian", "", path)
	if err != nil {
		t.Fatalf("AddLibrary: %v", err)
	}
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
	list, err := s.Libraries()
	if err != nil {
		t.Fatalf("Libraries: %v", err)
	}
	if !strings.Contains(list, `"id":"obsidian"`) {
		t.Errorf("Libraries = %s, want it to list the added library", list)
	}
}

func TestAddLibraryUnknownPresetFails(t *testing.T) {
	s := openTest(t, "iPhone")
	if _, err := s.AddLibrary("does-not-exist", "", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("AddLibrary with an unknown preset succeeded, want an error")
	}
}

func TestPauseResumeRemoveRoundTrip(t *testing.T) {
	s := openTest(t, "iPhone")
	path := filepath.Join(t.TempDir(), "vault")
	if _, err := s.AddLibrary("obsidian", "", path); err != nil {
		t.Fatalf("AddLibrary: %v", err)
	}
	if err := s.PauseLibrary("obsidian"); err != nil {
		t.Fatalf("PauseLibrary: %v", err)
	}
	if list, err := s.Libraries(); err != nil || !strings.Contains(list, `"paused":true`) {
		t.Errorf("Libraries after pause = %s, err %v; want paused true", list, err)
	}
	if err := s.ResumeLibrary("obsidian"); err != nil {
		t.Fatalf("ResumeLibrary: %v", err)
	}
	if list, err := s.Libraries(); err != nil || strings.Contains(list, `"paused":true`) {
		t.Errorf("Libraries after resume = %s, err %v; want no paused library", list, err)
	}
	if err := s.RemoveLibrary("obsidian"); err != nil {
		t.Fatalf("RemoveLibrary: %v", err)
	}
	if list, err := s.Libraries(); err != nil || list != "[]" {
		t.Errorf("Libraries after remove = %s, err %v; want []", list, err)
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

	offerJSON, err := a.StartPairing()
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	var offer struct {
		DeviceID string `json:"deviceId"`
		QR       string `json:"qr"`
	}
	if err := json.Unmarshal([]byte(offerJSON), &offer); err != nil {
		t.Fatalf("StartPairing returned bad JSON: %v\n%s", err, offerJSON)
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
	if _, err := a.AddLibrary("obsidian", "", aPath); err != nil {
		t.Fatalf("AddLibrary on Mac: %v", err)
	}
	if _, err := b.AddLibrary("obsidian", "", bPath); err != nil {
		t.Fatalf("AddLibrary on iPhone: %v", err)
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
