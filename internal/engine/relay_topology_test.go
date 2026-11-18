package engine

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	synkrelay "github.com/bristlecone-zSuite/zSynk/relay"
)

// syncThrough drives one library's sync between two paired devices: both
// dial at the same time, because a relay room only exists while both
// sides are in it.
func syncThrough(x, y *Node, folder string) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = x.syncPeer(y.Device.DeviceID, folder) }()
	go func() { defer wg.Done(); errs[1] = y.syncPeer(x.Device.DeviceID, folder) }()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Three devices, one relay, no direct addresses anywhere: a file moves
// from B to C through A (two rooms, one per pair), and one from C to B
// the same way. The relay is never recorded as anyone's address.
func TestThreeDevicesSyncThroughOneRelay(t *testing.T) {
	a := openNode(t, "Studio Mac")
	b := openNode(t, "Field iPhone")
	c := openNode(t, "Cabin iPad")
	must(t, Pair(a, b))
	must(t, Pair(a, c))

	srv := synkrelay.NewServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	go func() { _ = (&http.Server{Handler: srv.Handler()}).Serve(ln) }()

	for _, n := range []*Node{a, b, c} {
		locked(n, func() { n.Config.Relay = "ws://" + ln.Addr().String() })
	}

	dirA, dirB, dirC := library(t, a, "notes"), library(t, b, "notes"), library(t, c, "notes")

	writeFile(t, dirB, "from-b.md", "b through a to c")
	must(t, syncThrough(a, b, "notes"))
	wantFile(t, dirA, "from-b.md", "b through a to c")
	must(t, syncThrough(a, c, "notes"))
	wantFile(t, dirC, "from-b.md", "b through a to c")

	writeFile(t, dirC, "from-c.md", "c through a to b")
	must(t, syncThrough(a, c, "notes"))
	wantFile(t, dirA, "from-c.md", "c through a to b")
	must(t, syncThrough(a, b, "notes"))
	wantFile(t, dirB, "from-c.md", "c through a to b")

	for _, x := range []struct{ n, peer *Node }{{a, b}, {a, c}, {b, a}, {c, a}} {
		locked(x.n, func() {
			p := x.n.Config.PeerByID(x.peer.Device.DeviceID)
			if p == nil {
				t.Fatal("the peer vanished")
			}
			if len(p.Addresses) != 0 {
				t.Fatalf("relay recorded addresses %v", p.Addresses)
			}
		})
	}
}

// A pair with two shared libraries syncs both through the relay at the
// same time. The rooms are per library (decision 0013): a file written
// into one library must never land in the other one, whatever the
// timing of the two sessions is.
func TestTwoLibrariesOfAPairUseSeparateRooms(t *testing.T) {
	a, b := openNode(t, "Studio Mac"), openNode(t, "Field iPhone")
	must(t, Pair(a, b))

	srv := synkrelay.NewServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	go func() { _ = (&http.Server{Handler: srv.Handler()}).Serve(ln) }()

	for _, n := range []*Node{a, b} {
		locked(n, func() { n.Config.Relay = "ws://" + ln.Addr().String() })
	}

	notesA, notesB := library(t, a, "notes"), library(t, b, "notes")
	diaryA, diaryB := library(t, a, "diary"), library(t, b, "diary")

	writeFile(t, notesA, "note.md", "belongs in notes")
	writeFile(t, diaryB, "entry.md", "belongs in diary")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = syncThrough(a, b, "notes") }()
	go func() { defer wg.Done(); syncThrough(a, b, "diary") }()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			must(t, err)
		}
	}

	wantFile(t, notesB, "note.md", "belongs in notes")
	wantFile(t, diaryA, "entry.md", "belongs in diary")
	if _, err := os.Stat(filepath.Join(notesB, "entry.md")); !os.IsNotExist(err) {
		t.Fatalf("entry.md crossed into notes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(diaryA, "note.md")); !os.IsNotExist(err) {
		t.Fatalf("note.md crossed into diary: %v", err)
	}
}
