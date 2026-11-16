package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
)

// Pausing and resuming a library goes through the API, and an unknown
// library is a 404.
func TestPauseAndResumeLibrary(t *testing.T) {
	s := newServer(t, "Mac")
	must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: t.TempDir() + "/notes"}))

	w := serve(s, request(s, http.MethodPost, "/v1/libraries/notes/pause", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	for _, l := range s.Node.Libraries() {
		if l.ID == "notes" && !l.Paused {
			t.Fatal("the library isn't paused")
		}
	}
	w = serve(s, request(s, http.MethodPost, "/v1/libraries/notes/resume", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	for _, l := range s.Node.Libraries() {
		if l.ID == "notes" && l.Paused {
			t.Fatal("the library is still paused")
		}
	}
	if w := serve(s, request(s, http.MethodPost, "/v1/libraries/nope/pause", "")); w.Code != http.StatusNotFound {
		t.Fatalf("pausing an unknown library: %d", w.Code)
	}
}

// Removing a library through the API keeps its files and versions.
func TestRemoveLibraryKeepsEverything(t *testing.T) {
	s := newServer(t, "Mac")
	dir := t.TempDir() + "/notes"
	must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: dir}))
	must(t, os.WriteFile(dir+"/a.md", []byte("hello"), 0o600))
	if _, err := s.Node.ScanLibrary("notes"); err != nil {
		t.Fatal(err)
	}
	if w := serve(s, request(s, http.MethodDelete, "/v1/libraries/notes", "")); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if len(s.Node.Libraries()) != 0 {
		t.Fatal("the removed library is still configured")
	}
	if _, err := os.Stat(dir + "/a.md"); err != nil {
		t.Fatal(err)
	}
	if w := serve(s, request(s, http.MethodDelete, "/v1/libraries/notes", "")); w.Code != http.StatusNotFound {
		t.Fatalf("removing it again: %d", w.Code)
	}
}

// Unpairing through the API removes the device.
func TestUnpair(t *testing.T) {
	s := newServer(t, "Mac")
	s.Node.Config.UpsertPeer(config.Peer{DeviceID: "QQKX-AHEH-BZYC-WDA9", Name: "Studio Mac", PairedAt: time.Now().UTC()})
	if w := serve(s, request(s, http.MethodDelete, "/v1/peers/QQKX-AHEH-BZYC-WDA9", "")); w.Code != http.StatusOK {
		t.Fatalf("unpair: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.Node.Peer("QQKX-AHEH-BZYC-WDA9"); ok {
		t.Fatal("the unpaired device is still configured")
	}
	if w := serve(s, request(s, http.MethodDelete, "/v1/peers/QQKX-AHEH-BZYC-WDA9", "")); w.Code != http.StatusNotFound {
		t.Fatalf("unpairing again: %d", w.Code)
	}
}

// The event stream carries what happens as it happens, and ends when
// the reader hangs up.
func TestEventsStream(t *testing.T) {
	s := newServer(t, "Mac")
	must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Label: "n", Path: t.TempDir() + "/notes"}))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/events", nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := srv.Client().Do(req)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", resp.StatusCode)
	}
	rd := bufio.NewReader(resp.Body)
	line, err := rd.ReadString('\n')
	must(t, err)
	if line != "retry: 3000\n" {
		t.Fatalf("the stream opened with %q", line)
	}

	// What happens on the device arrives as a data line.
	go func() { _ = s.Node.PauseLibrary("notes") }()
	var line2 string
	for {
		line2, err = rd.ReadString('\n')
		must(t, err)
		if line2 != "\n" {
			break
		}
	}
	if line2[:6] != "data: " {
		t.Fatalf("the event arrived as %q", line2)
	}
	var e engine.Event
	must(t, json.Unmarshal([]byte(line2[6:]), &e))
	if e.Kind != "library" || e.Library != "notes" {
		t.Fatalf("the pause published %+v", e)
	}

	// Hanging up ends the handler.
	cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := rd.ReadString('\n'); err != nil {
				close(done)
				return
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hanging up didn't end the stream")
	}
}
