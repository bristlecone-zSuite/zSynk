package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
)

func TestNetworkPairAndSync(t *testing.T) {
	a, b := openNode(t, "Studio\tMac"), openNode(t, "Field\niPhone")
	addrB := listen(t, b)
	addrA := listen(t, a)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	if p, ok := a.peerByKey(public(t, b)); !ok || p.Name != "Field iPhone" || !slices.Equal(p.Addresses, []string{addrB}) {
		t.Fatalf("A's record of B: %+v, %v", p, ok)
	}
	if p, ok := b.peerByKey(public(t, a)); !ok || p.Name != "Studio Mac" || !slices.Equal(p.Addresses, []string{addrA}) {
		t.Fatalf("B's record of A: %+v, %v", p, ok)
	}
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "hello.md", "network")
	must(t, b.SyncWith(addrA, "notes"))
	wantFile(t, dirB, "hello.md", "network")
}

// Files of any size sync, and neither device holds one in memory.
func TestLargeFilesSyncOverNetwork(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "small.md", "small")
	big := bytes.Repeat([]byte("k"), 32*transport.ChunkSize+1)
	must(t, os.WriteFile(filepath.Join(dirA, "talk.mov"), big, 0o600))

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	must(t, b.SyncWith(addrA, "notes"))
	runtime.ReadMemStats(&after)

	wantFile(t, dirB, "small.md", "small")
	got, err := os.ReadFile(filepath.Join(dirB, "talk.mov"))
	must(t, err)
	if !bytes.Equal(got, big) {
		t.Fatalf("talk.mov arrived with %d bytes, want %d", len(got), len(big))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > uint64(len(big))/2 {
		t.Fatalf("syncing a %d MiB file allocated %d MiB", len(big)>>20, alloc>>20)
	}
}

// Indexes longer than a frame cross in both directions.
func TestLargeIndexesSyncOverNetwork(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	long := strings.Repeat("x", 200)
	for i := range 800 {
		rel := fmt.Sprintf("%s/%s/%03d-%s.md", long, long, i, long)
		writeFile(t, dirA, rel, "both")
		writeFile(t, dirB, rel, "both")
	}
	writeFile(t, dirA, "from-mac.md", "mac")
	writeFile(t, dirB, "from-iphone.md", "iphone")
	must(t, b.SyncWith(addrA, "notes"))
	wantFile(t, dirB, "from-mac.md", "mac")
	wantFile(t, dirA, "from-iphone.md", "iphone")
	for _, n := range []*Node{a, b} {
		raw, err := encodeBody(transport.KindIndex, n.Index("notes"))
		must(t, err)
		if len(raw) <= transport.MaxFrame {
			t.Fatalf("%s's index is %d bytes, which fits in a frame", n.Device.Name, len(raw))
		}
	}
}

// Neither device sends or reads an index over maxBody, or reads one that
// isn't JSON, and both say why the sync failed.
func TestUnusableIndexesFail(t *testing.T) {
	defer func(n int64) { maxBody = n }(maxBody)
	maxBody = 2048
	waitForError := func(t *testing.T, a, b *Node, want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(deviceSync(t, a, "notes", b).Error, want); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("A recorded %+v; want an error about %q", deviceSync(t, a, "notes", b), want)
			}
		}
	}
	for _, large := range []string{"answering", "starting"} {
		t.Run(large, func(t *testing.T) {
			a, b := openNode(t, "Mac"), openNode(t, "iPhone")
			must(t, Pair(a, b))
			addrA := listen(t, a)
			dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
			dir := dirA
			if large == "starting" {
				dir = dirB
			}
			for i := range 20 {
				writeFile(t, dir, fmt.Sprintf("n%02d.md", i), "x")
			}
			const want = "more than the 2048 another device reads"
			if err := b.SyncWith(addrA, "notes"); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("B: %v; want an error about %q", err, want)
			}
			waitForError(t, a, b, want)
		})
	}
	for name, sent := range map[string]struct {
		body []byte
		want string
	}{
		"over the limit": {make([]byte, maxBody+1), "over the limit of 2048"},
		"not JSON":       {[]byte(`{"folderId":"notes"`), "engine: the index:"},
	} {
		t.Run(name, func(t *testing.T) {
			a, b := openNode(t, "Mac"), openNode(t, "iPhone")
			must(t, Pair(a, b))
			addrA := listen(t, a)
			library(t, a, "notes")
			c, err := transport.Dial(addrA, time.Second, transport.Config(b.cert, b.anyPeer))
			must(t, err)
			defer c.Close()
			must(t, c.SetDeadline(time.Now().Add(5*time.Second)))
			must(t, transport.WriteMsg(c, transport.Msg{Kind: transport.KindHello, FolderID: "notes"}))
			if m, err := transport.ReadMsg(c); err != nil || m.Kind != transport.KindHelloOK {
				t.Fatalf("%+v, %v; want %s", m, err, transport.KindHelloOK)
			}
			var theirs reconcile.Index
			must(t, readBody(c, transport.KindIndex, &theirs))
			must(t, transport.WriteBody(c, transport.Msg{Kind: transport.KindIndex, FolderID: "notes"}, sent.body))
			waitForError(t, a, b, sent.want)
		})
	}
}

// A session can take longer than idleTimeout in all, as long as data
// keeps moving.
func TestSlowSessionsFinish(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	a.idleTimeout, b.idleTimeout = 300*time.Millisecond, 300*time.Millisecond
	must(t, Pair(a, b))
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	content := strings.Repeat("v", 2<<20)
	writeFile(t, dirA, "talk.mov", content)
	slow := relay(t, listen(t, a), func(up bool, _ int64, _ []byte) {
		if !up {
			time.Sleep(20 * time.Millisecond)
		}
	})
	start := time.Now()
	must(t, b.SyncWith(slow, "notes"))
	if took := time.Since(start); took < 2*a.idleTimeout {
		t.Fatalf("the session took %v, too little to show it can outlast idleTimeout", took)
	}
	wantFile(t, dirB, "talk.mov", content)
}

// A session ends once the other device stops responding for idleTimeout.
func TestStalledSessionsEnd(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	a.idleTimeout, b.idleTimeout = 300*time.Millisecond, 300*time.Millisecond
	must(t, Pair(a, b))
	dirA, _ := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "talk.mov", strings.Repeat("v", 4<<20))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stalled := relay(t, listen(t, a), func(up bool, off int64, _ []byte) {
		if !up && off > 256<<10 {
			<-release
		}
	})
	errc := make(chan error, 1)
	go func() { errc <- b.SyncWith(stalled, "notes") }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a stalled session succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled session didn't end")
	}
}

// An active connection's deadline moves on with each part of a write and
// each read, so slow transfers that keep moving finish and stalled ones
// end.
func TestActiveConnections(t *testing.T) {
	const idle = 250 * time.Millisecond
	pipe := func() (net.Conn, net.Conn) {
		near, far := net.Pipe()
		t.Cleanup(func() { _ = near.Close(); _ = far.Close() })
		return near, far
	}

	near, far := pipe()
	go func() {
		buf := make([]byte, activeWrite)
		for {
			time.Sleep(idle / 10)
			if _, err := far.Read(buf); err != nil {
				return
			}
		}
	}()
	start := time.Now()
	if _, err := (active{near, idle}).Write(make([]byte, 20*activeWrite)); err != nil {
		t.Fatalf("a slow write that kept moving: %v", err)
	}
	if took := time.Since(start); took < idle {
		t.Fatalf("the write took %v, too little to show it can outlast idle", took)
	}

	for name, stall := range map[string]func(c net.Conn) error{
		"read":  func(c net.Conn) error { _, err := c.Read(make([]byte, 1)); return err },
		"write": func(c net.Conn) error { _, err := c.Write([]byte("x")); return err },
	} {
		near, _ := pipe()
		errc := make(chan error, 1)
		go func() { errc <- stall(active{near, idle}) }()
		select {
		case err := <-errc:
			if err == nil {
				t.Errorf("a stalled %s succeeded", name)
			}
		case <-time.After(10 * idle):
			t.Errorf("a stalled %s didn't end", name)
		}
	}
}

// A device that finds content in its store corrupt when it's asked for it
// removes it, and the next scan stores the file's content again.
func TestCorruptContentHeals(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "a.md", "original")
	_, err := a.ScanLibrary("notes")
	must(t, err)
	h := atomicfile.HashBytes([]byte("original"))
	must(t, os.WriteFile(filepath.Join(a.Store.Root, "objects", h[:2], h[2:4], h), []byte("tampered"), 0o600))

	if err := b.SyncWith(addrA, "notes"); err == nil {
		t.Fatal("B synced content that was corrupt in A's store")
	}
	if a.Store.Has(h) {
		t.Fatal("A kept content it found corrupt")
	}
	if _, err := os.Stat(filepath.Join(dirB, "a.md")); err == nil {
		t.Fatal("B wrote content that didn't match its hash")
	}
	must(t, b.SyncWith(addrA, "notes"))
	wantFile(t, dirB, "a.md", "original")
}

// A sync succeeds only if both devices apply what they received. Each
// applies its own changes either way.
func TestSyncReportsTheOtherDevicesFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only folder")
	}
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "from-a.md", "a")
	writeFile(t, dirA, "ro-b/from-a.md", "a")
	writeFile(t, dirB, "ro-a/from-b.md", "b")
	for _, dir := range []string{filepath.Join(dirA, "ro-a"), filepath.Join(dirB, "ro-b")} {
		must(t, os.Mkdir(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}

	err := b.SyncWith(addrA, "notes")
	if err == nil || !strings.Contains(err.Error(), "Mac couldn't apply") {
		t.Fatalf("syncing with a device that couldn't apply the changes: %v", err)
	}
	if !strings.Contains(err.Error(), "ro-b/from-a.md") {
		t.Fatalf("B's sync didn't report what B couldn't apply: %v", err)
	}
	wantFile(t, dirB, "from-a.md", "a")
}

func TestPairOfferSurvivesReopen(t *testing.T) {
	a := openNode(t, "Mac")
	b := openNode(t, "iPhone")
	sess := offer(t, a)
	must(t, a.Close())
	restarted := openNodeAt(t, a.DataDir, "Mac")
	joinOffer(t, b, listen(t, restarted), sess, "")
	if _, ok := restarted.Peer(b.Device.DeviceID); !ok {
		t.Fatal("restarted node missing peer")
	}
}

func TestOneNodePerDataFolder(t *testing.T) {
	dir := t.TempDir()
	daemon := openNodeAt(t, dir, "Mac")
	if cli, err := Open(dir, "Mac"); !errors.Is(err, ErrInUse) {
		if cli != nil {
			_ = cli.Close()
		}
		t.Fatalf("a second node opened a data folder in use: %v", err)
	}
	must(t, daemon.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes")}))
	must(t, daemon.Close())
	must(t, daemon.Close())
	reopened := openNodeAt(t, dir, "Mac")
	if _, ok := reopened.Library("notes"); !ok {
		t.Fatal("the library wasn't saved")
	}
	must(t, reopened.Close())

	cfg := config.Path(dir)
	good, err := os.ReadFile(cfg)
	must(t, err)
	must(t, os.WriteFile(cfg, []byte("{"), 0o600))
	if n, err := Open(dir, "Mac"); err == nil || errors.Is(err, ErrInUse) {
		if n != nil {
			_ = n.Close()
		}
		t.Fatalf("opened with a corrupt config: %v", err)
	}
	must(t, os.WriteFile(cfg, good, 0o600))
	openNodeAt(t, dir, "Mac")
}

// The daemon's loop syncs while the API pairs; the race detector checks
// that they share the config safely.
func TestPairingWhileSyncingIsRaceFree(t *testing.T) {
	a := openNode(t, "A")
	b := openNode(t, "B")
	addrA := listen(t, a)
	library(t, b, "notes")
	sess := offer(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	loop := make(chan struct{})
	go func() {
		defer close(loop)
		for ctx.Err() == nil {
			_ = b.SyncAllPeers()
			_ = b.Status()
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := b.JoinPeer(addrA, sess.Code, sess.Token, a.Fingerprint())
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-loop
	must(t, err)
	if _, ok := b.Peer(a.Device.DeviceID); !ok {
		t.Fatal("not paired")
	}
}

// The race detector counts socket and file I/O as synchronization, so this
// reads the config while pairs are added without any I/O on the reading
// side.
func TestConfigReadsRaceFreeWithPairing(t *testing.T) {
	a := openNode(t, "A")
	keys := make([]ed25519.PublicKey, 50)
	for i := range keys {
		pub, _, err := ed25519.GenerateKey(nil)
		must(t, err)
		keys[i] = pub
	}
	far := netip.MustParseAddr("192.0.2.1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, k := range keys {
			_, _ = a.peerByKey(k)
			_ = a.anyPeer(k)
			_ = a.shared("notes", identity.IDFromPublic(k))
			_ = a.peerHostAllowed(far)
			_ = a.Status()
		}
	}()
	for _, k := range keys {
		id := identity.IDFromPublic(k)
		must(t, a.addPeer(config.Peer{DeviceID: id, Name: "peer", PublicKey: identity.EncodePublic(k)}))
		a.peerSeen(id, "192.0.2.1:7420")
	}
	<-done
}

func TestUnpairedDeviceCannotSync(t *testing.T) {
	a, b, stranger := openNode(t, "Mac"), openNode(t, "iPhone"), openNode(t, "Stranger")
	addrA := listen(t, a)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	writeFile(t, library(t, a, "notes"), "diary.md", "private")
	library(t, stranger, "notes")
	if err := stranger.SyncWith(addrA, "notes"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("synced with a device it never paired with: %v", err)
	}

	c, err := transport.Dial(addrA, time.Second, transport.Config(stranger.cert, anyKey))
	must(t, err)
	defer c.Close()
	must(t, transport.WriteMsg(c, transport.Msg{Kind: transport.KindHello, FolderID: "notes"}))
	resp, err := transport.ReadMsg(c)
	if err != nil || resp.Kind != transport.KindError {
		t.Fatalf("A answered an unpaired device's hello with %+v, %v", resp, err)
	}
}

// B checks the key of the device it reaches before it shows its own, so a
// device it never paired with doesn't learn who B is. A paired device at
// another one's address isn't taken for that device.
func TestSyncShowsNothingToOtherDevices(t *testing.T) {
	a, b, c := openNode(t, "Mac"), openNode(t, "iPhone"), openNode(t, "iPad")
	must(t, Pair(a, b))
	must(t, Pair(b, c))
	dirB := library(t, b, "notes")
	writeFile(t, library(t, c, "notes"), "plan.md", "for the iPad")

	var shown bool
	addr, done := acceptAs(t, openNode(t, "Stranger"), func(conn *tls.Conn) {
		shown = conn.Handshake() == nil || len(conn.ConnectionState().PeerCertificates) > 0
	})
	if err := b.SyncWith(addr, "notes"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("B synced with a device it never paired with: %v", err)
	}
	<-done
	if shown {
		t.Fatal("B showed its key to a device it never paired with")
	}

	addrC := listen(t, c)
	locked(b, func() { b.Config.PeerByID(a.Device.DeviceID).Addresses = []string{addrC} })
	if err := b.syncPeer(a.Device.DeviceID, "notes"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("B took the iPad for the Mac: %v", err)
	}
	wantGone(t, dirB, "plan.md")
}

// Someone watching the network sees neither files nor pairing secrets.
func TestPeerTrafficIsPrivate(t *testing.T) {
	a, b := openNode(t, "Studio Mac"), openNode(t, "Field iPhone")
	addrA := listen(t, a)
	var mu sync.Mutex
	var wire bytes.Buffer
	tapped := relay(t, addrA, func(_ bool, _ int64, p []byte) {
		mu.Lock()
		defer mu.Unlock()
		wire.Write(p)
	})
	sess := offer(t, a)
	joinOffer(t, b, tapped, sess, a.Fingerprint())
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	const secret = "private journal entry"
	writeFile(t, dirA, "diary.md", secret)
	must(t, b.SyncWith(tapped, "notes"))
	wantFile(t, dirB, "diary.md", secret)

	mu.Lock()
	defer mu.Unlock()
	for _, s := range []string{secret, "diary.md", sess.Token, sess.Code, pairing.Normalize(sess.Code), "Studio Mac", "Field iPhone", a.Device.DeviceID, `"kind"`} {
		if bytes.Contains(wire.Bytes(), []byte(s)) {
			t.Errorf("%q crossed the network in the clear", s)
		}
	}
}

// Someone on the network path who changes what A sends gets B to apply
// none of it.
func TestTamperedTrafficIsRejected(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeNotes(t, dirA, 0, 300)
	// Past the handshake, in A's index.
	const at = 16 << 10
	var flipped atomic.Bool
	tampered := relay(t, addrA, func(up bool, off int64, p []byte) {
		if !up && off <= at && at < off+int64(len(p)) {
			p[at-off] ^= 1
			flipped.Store(true)
		}
	})
	err := b.SyncWith(tampered, "notes")
	if !flipped.Load() {
		t.Fatalf("A sent B fewer than %d bytes", at)
	}
	if err == nil || !strings.Contains(err.Error(), "bad record MAC") {
		t.Fatalf("B took tampered data: %v", err)
	}
	for i := range 300 {
		wantGone(t, dirB, note(i))
	}
	must(t, b.SyncWith(addrA, "notes"))
	wantFile(t, dirB, note(299), body(299))
}

// Someone who recorded a session and sends it again gets nothing.
func TestReplayedSessionIsRefused(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	writeFile(t, library(t, a, "notes"), "diary.md", "private journal entry")
	library(t, b, "notes")
	var mu sync.Mutex
	var sent bytes.Buffer
	recorded := relay(t, addrA, func(up bool, _ int64, p []byte) {
		if up {
			mu.Lock()
			defer mu.Unlock()
			sent.Write(p)
		}
	})
	must(t, b.SyncWith(recorded, "notes"))
	p, _ := a.Peer(b.Device.DeviceID)
	seen := p.LastSeen
	if seen.IsZero() {
		t.Fatal("A didn't note when it saw B")
	}

	c, err := net.Dial("tcp", addrA)
	must(t, err)
	defer c.Close()
	must(t, c.SetDeadline(time.Now().Add(5*time.Second)))
	mu.Lock()
	_, err = c.Write(sent.Bytes())
	mu.Unlock()
	must(t, err)
	var ne net.Error
	if _, err := io.Copy(io.Discard, c); errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("A kept a replayed session open")
	}
	if p, _ := a.Peer(b.Device.DeviceID); !p.LastSeen.Equal(seen) {
		t.Fatal("A served a replayed session")
	}
}

// A device in the middle of a pairing, with its own key toward each end,
// gets neither end to accept it, whether it relays what they send or
// answers itself.
func TestPairingThroughADeviceInTheMiddleFails(t *testing.T) {
	a, b, m := openNode(t, "Mac"), openNode(t, "iPhone"), openNode(t, "Mallory")
	addrA := listen(t, a)
	sess := offer(t, a)
	guess, err := pairing.NewSession()
	must(t, err)

	relayed, done := acceptAs(t, m, func(c *tls.Conn) {
		req, err := transport.ReadMsg(c)
		if err != nil {
			return
		}
		up, err := transport.Dial(addrA, time.Second, transport.Config(m.cert, anyKey))
		if err != nil {
			return
		}
		defer up.Close()
		if transport.WriteMsg(up, req) != nil {
			return
		}
		if reply, err := transport.ReadMsg(up); err == nil {
			_ = transport.WriteMsg(c, reply)
		}
	})
	if err := b.JoinPeer(relayed, sess.Code, sess.Token, ""); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("B paired through a relay: %v", err)
	}
	<-done

	for name, forge := range map[string]func(req transport.Msg, binding []byte) string{
		"a proof without the token": func(_ transport.Msg, binding []byte) string {
			p, _ := pairing.Proof(sess.Code, guess.Token, pairing.RoleAccept, binding)
			return p
		},
		"B's own proof": func(req transport.Msg, _ []byte) string { return req.Proof },
	} {
		addr, done := acceptAs(t, m, func(c *tls.Conn) {
			req, err := transport.ReadMsg(c)
			if err != nil {
				return
			}
			binding, _ := transport.Binding(c.ConnectionState())
			_ = transport.WriteMsg(c, transport.Msg{Kind: transport.KindPairOK, Name: "Mac", Proof: forge(req, binding)})
		})
		if err := b.JoinPeer(addr, sess.Code, sess.Token, ""); !errors.Is(err, ErrPairingRejected) {
			t.Errorf("B paired with a device that answered with %s: %v", name, err)
		}
		<-done
	}

	got := make(chan bool, 1)
	pinned, done := acceptAs(t, m, func(c *tls.Conn) {
		_, err := transport.ReadMsg(c)
		got <- err == nil
	})
	if err := b.JoinPeer(pinned, sess.Code, sess.Token, a.Fingerprint()); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("B paired with a key other than the offer's: %v", err)
	}
	<-done
	select {
	case read := <-got:
		if read {
			t.Fatal("B sent its proof to a device without the offer's key")
		}
	default:
	}

	for _, n := range []*Node{a, b, m} {
		if peers := n.Status().Peers; len(peers) != 0 {
			t.Fatalf("%s paired with %+v", n.Device.Name, peers)
		}
	}
	joinOffer(t, b, addrA, sess, a.Fingerprint())
}

// One offer pairs one device, even when several try at once.
func TestOfferPairsOneDevice(t *testing.T) {
	a := openNode(t, "Mac")
	addrA := listen(t, a)
	sess := offer(t, a)
	joiners := make([]*Node, maxConnsPerHost)
	for i := range joiners {
		joiners[i] = openNode(t, fmt.Sprintf("Phone %d", i))
	}
	errs := make([]error, len(joiners))
	var wg sync.WaitGroup
	for i, n := range joiners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = n.JoinPeer(addrA, sess.Code, sess.Token, a.Fingerprint())
		}()
	}
	wg.Wait()
	paired := 0
	for i, err := range errs {
		switch {
		case err == nil:
			paired++
		case !errors.Is(err, ErrPairingRejected):
			t.Errorf("phone %d: %v", i, err)
		}
	}
	if peers := a.Status().Peers; paired != 1 || len(peers) != 1 {
		t.Fatalf("%d devices paired with one offer, and A has %d peers", paired, len(peers))
	}
	if _, ok := a.PairingOffer(); ok {
		t.Fatal("the offer still waits after a device used it")
	}
	if p, err := pairing.LoadPending(a.DataDir); p != nil || err != nil {
		t.Fatalf("the used offer is still saved: %+v, %v", p, err)
	}
}

func TestPairingRejections(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	sess := offer(t, a)
	guess, err := pairing.NewSession()
	must(t, err)
	for name, try := range map[string][3]string{
		"a wrong code":            {guess.Code, sess.Token, a.Fingerprint()},
		"a wrong token":           {sess.Code, guess.Token, a.Fingerprint()},
		"a malformed token":       {sess.Code, "not-a-token", a.Fingerprint()},
		"another fingerprint":     {sess.Code, sess.Token, b.Fingerprint()},
		"a malformed fingerprint": {sess.Code, sess.Token, "abc123"},
	} {
		if err := b.JoinPeer(addrA, try[0], try[1], try[2]); !errors.Is(err, ErrPairingRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(a.Status().Peers) != 0 || len(b.Status().Peers) != 0 {
		t.Fatal("a failed pairing paired the devices")
	}
	// Failed attempts don't use up the offer, and codes and fingerprints
	// are read as people type them.
	fp := strings.ToUpper(a.Fingerprint())
	joinOffer(t, b, addrA, &pairing.Session{Code: strings.ToLower(sess.Code), Token: sess.Token}, fp[:8]+":"+fp[8:])
}

func TestOfferExpires(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	sess := offer(t, a)
	locked(a, func() {
		if a.offer == nil {
			t.Fatal("no offer waits")
		}
		a.offer.ExpiresAt = time.Now().Add(-time.Second)
	})
	if _, ok := a.PairingOffer(); ok {
		t.Fatal("an expired offer still waits")
	}
	if err := b.JoinPeer(addrA, sess.Code, sess.Token, a.Fingerprint()); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("paired with an expired offer: %v", err)
	}
}

// If A can't save a pairing, neither device uses it, and the offer still
// waits.
func TestUnsavedPairingChangesNothing(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	sess := offer(t, a)
	cfg := config.Path(a.DataDir)
	saved, err := os.ReadFile(cfg)
	must(t, err)
	must(t, os.Remove(cfg))
	must(t, os.MkdirAll(filepath.Join(cfg, "blocked"), 0o700))
	if err := b.JoinPeer(addrA, sess.Code, sess.Token, a.Fingerprint()); err == nil {
		t.Fatal("B paired although A couldn't save the pairing")
	}
	if len(a.Status().Peers) != 0 || len(b.Status().Peers) != 0 {
		t.Fatal("a pairing that wasn't saved is in use")
	}
	if _, ok := a.PairingOffer(); !ok {
		t.Fatal("a pairing that wasn't saved used up the offer")
	}
	must(t, os.RemoveAll(cfg))
	must(t, os.WriteFile(cfg, saved, 0o600))
	joinOffer(t, b, addrA, sess, a.Fingerprint())
}

// A device's first frame is small, whoever it is, so strangers can't make
// a device allocate much.
func TestFirstFrameIsSmall(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	library(t, a, "notes")
	hello := func(name string) transport.Msg {
		c, err := transport.Dial(addrA, time.Second, transport.Config(b.cert, b.anyPeer))
		must(t, err)
		defer c.Close()
		must(t, c.SetDeadline(time.Now().Add(5*time.Second)))
		if err := transport.WriteMsg(c, transport.Msg{Kind: transport.KindHello, FolderID: "notes", Name: name}); err != nil {
			return transport.Msg{}
		}
		resp, _ := transport.ReadMsg(c)
		return resp
	}
	if resp := hello("iPhone"); resp.Kind != transport.KindHelloOK {
		t.Fatalf("a small hello: %+v", resp)
	}
	if resp := hello(strings.Repeat("x", transport.MaxPreAuthFrame)); resp.Kind == transport.KindHelloOK {
		t.Fatalf("A read a first frame over %d bytes", transport.MaxPreAuthFrame)
	}
}

// A connection that doesn't finish its handshake and first frame within
// handshakeTimeout is closed; a paired device's session then gets longer.
func TestSilentConnectionsTimeOut(t *testing.T) {
	defer func(d time.Duration) { handshakeTimeout = d }(handshakeTimeout)
	handshakeTimeout = 300 * time.Millisecond
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	library(t, a, "notes")

	silent, err := net.Dial("tcp", addrA)
	must(t, err)
	defer silent.Close()
	must(t, silent.SetReadDeadline(time.Now().Add(5*time.Second)))
	var ne net.Error
	if _, err := silent.Read(make([]byte, 1)); err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("A kept a silent connection open: %v", err)
	}

	c, err := transport.Dial(addrA, time.Second, transport.Config(b.cert, b.anyPeer))
	must(t, err)
	defer c.Close()
	must(t, transport.WriteMsg(c, transport.Msg{Kind: transport.KindHello, FolderID: "notes"}))
	time.Sleep(3 * handshakeTimeout)
	expect := func(k transport.Kind) {
		t.Helper()
		if m, err := transport.ReadMsg(c); err != nil || m.Kind != k {
			t.Fatalf("a paired session after handshakeTimeout: %+v, %v; want %s", m, err, k)
		}
	}
	expect(transport.KindHelloOK)
	var theirs reconcile.Index
	must(t, readBody(c, transport.KindIndex, &theirs))
	idx, err := encodeBody(transport.KindIndex, reconcile.NewIndex("notes", b.Device.DeviceID))
	must(t, err)
	must(t, transport.WriteBody(c, transport.Msg{Kind: transport.KindIndex, FolderID: "notes"}, idx))
	must(t, writeWant(c, nil))
	var wants []string
	must(t, readBody(c, transport.KindWant, &wants))
	expect(transport.KindDone)
}

func TestOneHostGetsFewConnections(t *testing.T) {
	a := openNode(t, "Mac")
	addrA := listen(t, a)
	dial := func() net.Conn {
		c, err := net.Dial("tcp", addrA)
		must(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	open := func(c net.Conn) bool {
		must(t, c.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		_, err := c.Read(make([]byte, 1))
		var ne net.Error
		return errors.As(err, &ne) && ne.Timeout()
	}
	held := make([]net.Conn, maxConnsPerHost)
	for i := range held {
		held[i] = dial()
	}
	if open(dial()) {
		t.Fatalf("A took more than %d connections from one host", maxConnsPerHost)
	}
	for _, c := range held {
		if !open(c) {
			t.Fatal("A closed a connection within the host's limit")
		}
		_ = c.Close()
	}
	for deadline := time.Now().Add(5 * time.Second); !open(dial()); {
		if time.Now().After(deadline) {
			t.Fatal("A took no more connections from the host after its others closed")
		}
	}
}

// listen serves n's peer protocol on a loopback port until the test ends.
func listen(t testing.TB, n *Node) string {
	t.Helper()
	ln, err := n.ServePeers("127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

// offer makes a new pairing offer on n.
func offer(t *testing.T, n *Node) *pairing.Session {
	t.Helper()
	sess, err := pairing.NewSession()
	must(t, err)
	must(t, n.ArmPairing(sess.Code, sess.Token))
	return sess
}

// joinOffer pairs n with the device at addr that made sess.
func joinOffer(t *testing.T, n *Node, addr string, sess *pairing.Session, fingerprint string) {
	t.Helper()
	must(t, n.JoinPeer(addr, sess.Code, sess.Token, fingerprint))
}

// library enables a library on n, shared with the devices in share, or
// with every paired device, and returns its folder.
func library(t testing.TB, n *Node, id string, share ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	must(t, n.EnableLibrary(config.Library{ID: id, Preset: "custom", Path: dir, ShareWith: share}))
	return dir
}

func public(t *testing.T, n *Node) ed25519.PublicKey {
	t.Helper()
	pub, err := n.Device.Public()
	must(t, err)
	return pub
}

func anyKey(ed25519.PublicKey) error { return nil }

// acceptAs takes one connection on a new local address as n, which accepts
// any key, and hands it to serve. done closes when serve returns.
func acceptAs(t *testing.T, n *Node, serve func(c *tls.Conn)) (addr string, done <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		c := tls.Server(raw, n.serverTLS())
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		serve(c)
	}()
	return ln.Addr().String(), ch
}

// relay passes connections to target through a new local address. edit
// sees, and may change, what each side sends, at its offset in that
// direction, before it's passed on.
func relay(t *testing.T, target string, edit func(up bool, off int64, p []byte)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go pass(c, s, func(off int64, p []byte) { edit(true, off, p) })
			go pass(s, c, func(off int64, p []byte) { edit(false, off, p) })
		}
	}()
	return ln.Addr().String()
}

func pass(from, to net.Conn, edit func(off int64, p []byte)) {
	defer to.Close()
	defer from.Close()
	buf := make([]byte, 32<<10)
	var off int64
	for {
		n, err := from.Read(buf)
		if n > 0 {
			edit(off, buf[:n])
			off += int64(n)
			if _, err := to.Write(buf[:n]); err != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
