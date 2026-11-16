package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
)

// While `synk serve` runs, `synk enable` can't open the data folder, so it
// asks the daemon, and the daemon's later config saves keep the library.
func TestCLIChangesReachTheRunningDaemon(t *testing.T) {
	dir := t.TempDir()
	daemon := openNodeAt(t, dir, "Mac")
	c := connectTo(t, daemon)
	if n, err := engine.Open(dir, "Mac"); !errors.Is(err, engine.ErrInUse) {
		if n != nil {
			_ = n.Close()
		}
		t.Fatalf("the CLI opened the daemon's data folder: %v", err)
	}

	lib, err := c.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes")})
	must(t, err)
	if lib.ID != "notes" || lib.Label != "Any folder" {
		t.Fatalf("enabled %+v", lib)
	}
	if _, ok := daemon.Library("notes"); !ok {
		t.Fatal("the daemon doesn't have the library")
	}
	must(t, engine.Pair(daemon, openNode(t, "Phone")))
	must(t, daemon.Close())
	if _, ok := openNodeAt(t, dir, "Mac").Library("notes"); !ok {
		t.Fatal("a library enabled while the daemon ran was dropped when the daemon saved its config")
	}
}

func TestClientCalls(t *testing.T) {
	n := openNode(t, "Mac")
	c := connectTo(t, n)
	st, err := c.Status()
	must(t, err)
	if st.DeviceID != n.Device.DeviceID {
		t.Fatalf("status %+v", st)
	}
	offer, err := c.StartPairing()
	must(t, err)
	if offer.Code == "" || offer.DeviceID != n.Device.DeviceID || !strings.HasPrefix(offer.QR, "synk://pair?") {
		t.Fatalf("offer %+v", offer)
	}
	if waiting, err := c.PairingOffer(); err != nil || waiting.QR != offer.QR {
		t.Fatalf("the waiting offer: %+v, %v; made %+v", waiting, err, offer)
	}
	must(t, c.Sync("", ""))
	_, err = c.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes")})
	must(t, err)
	if paths, err := c.ConfirmDeletes("notes", nil); err != nil || len(paths) != 0 {
		t.Fatalf("confirm with nothing held: %v, %v", paths, err)
	}
	if r, err := c.RestoreDeletes("notes"); err != nil || len(r.Paths) != 0 || r.Warning != "" {
		t.Fatalf("restore with nothing held: %+v, %v", r, err)
	}
	lib, _ := n.Library("notes")
	must(t, os.WriteFile(filepath.Join(lib.Path, "a b.md"), []byte("new"), 0o600))
	_, err = n.Vault.SnapshotFile("notes", "a b.md", strings.NewReader("old"), atomicfile.HashBytes([]byte("old")), "pre-overwrite")
	must(t, err)
	vs, err := c.Versions("notes", "a b.md")
	must(t, err)
	if all, err := c.Versions("notes", ""); err != nil || len(vs) != 1 || !slices.Equal(all, vs) {
		t.Fatalf("versions of a b.md %+v; all versions %+v, %v", vs, all, err)
	}
	if ver, err := c.RestoreVersion("notes", "a b.md", vs[0].ID); err != nil || ver != vs[0] {
		t.Fatalf("restored %+v, %v; want %+v", ver, err, vs[0])
	}

	for name, c := range map[string]struct {
		err  error
		want int
	}{
		"an unknown library":      {func() error { _, err := c.RestoreDeletes("nope"); return err }(), http.StatusNotFound},
		"an unknown version":      {func() error { _, err := c.RestoreVersion("notes", "a b.md", "nope"); return err }(), http.StatusNotFound},
		"an unreachable peer":     {c.JoinPeer("127.0.0.1:1", offer.Code, offer.Token, ""), http.StatusBadGateway},
		"a malformed fingerprint": {c.JoinPeer("127.0.0.1:1", offer.Code, offer.Token, "abc"), http.StatusBadRequest},
		"a wrong token":           {func() error { _, err := (&Client{base: c.base, token: "x", http: c.http}).Status(); return err }(), http.StatusUnauthorized},
	} {
		var e *Error
		if !errors.As(c.err, &e) || e.Status != c.want || e.Message == "" {
			t.Errorf("%s: %v (%#v); want status %d", name, c.err, c.err, c.want)
		}
	}
}

func connectTo(t *testing.T, n *engine.Node) *Client {
	t.Helper()
	s, err := New(n)
	must(t, err)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c, err := Connect(n.DataDir, strings.TrimPrefix(srv.URL, "http://"))
	must(t, err)
	return c
}
