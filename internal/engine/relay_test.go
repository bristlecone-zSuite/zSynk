package engine

import (
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	synkrelay "github.com/bristlecone-zSuite/zSynk/relay"
)

// With no working direct address, both devices fall back to the pair's
// relay room: the device with the greater ID starts the session and the
// other answers, files cross in both directions, and the relay's address
// is never recorded as the peer's address (decision 0013).
func TestSyncFallsBackToRelay(t *testing.T) {
	a, b := openNode(t, "Studio Mac"), openNode(t, "Field iPhone")
	must(t, Pair(a, b))

	srv := synkrelay.NewServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	go func() { _ = (&http.Server{Handler: srv.Handler()}).Serve(ln) }()
	relayURL := "ws://" + ln.Addr().String()

	for _, n := range []*Node{a, b} {
		locked(n, func() { n.Config.Relay = relayURL })
	}

	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")

	// Both devices dial the relay; the room exists only while they are
	// both there, so the two attempts must overlap.
	syncBoth := func() error {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = a.syncPeer(b.Device.DeviceID, "notes") }()
		go func() { defer wg.Done(); errs[1] = b.syncPeer(a.Device.DeviceID, "notes") }()
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		return nil
	}

	writeFile(t, dirA, "hello.md", "through the relay")
	must(t, syncBoth())
	wantFile(t, dirB, "hello.md", "through the relay")

	// The connection through the relay came from the relay, so it must
	// not be recorded as the peer's address.
	for _, x := range []struct{ n, peer *Node }{{a, b}, {b, a}} {
		locked(x.n, func() {
			p := x.n.Config.PeerByID(x.peer.Device.DeviceID)
			if p == nil {
				t.Fatal("the peer vanished")
			}
			if len(p.Addresses) != 0 {
				t.Fatalf("relay recorded addresses %v", p.Addresses)
			}
			if time.Since(p.LastSeen) > time.Minute {
				t.Fatalf("last seen %v", p.LastSeen)
			}
		})
	}

	// And the next file crosses the other way.
	writeFile(t, dirB, "back.md", "and back")
	must(t, syncBoth())
	wantFile(t, dirA, "back.md", "and back")
}
