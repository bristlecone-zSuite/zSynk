package engine

import (
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
)

// Until a device has shown it's paired, it gets handshakeTimeout to finish
// the handshake and its first frame, and frames of at most
// transport.MaxPreAuthFrame.
var (
	handshakeTimeout = 10 * time.Second
	dialTimeout      = 5 * time.Second
)

// maxManifest bounds a package manifest from another device, which is
// read into memory. A manifest lists a package's files, not their content.
const maxManifest = 64 << 20

// Limits on concurrent connections from other devices, in all and from
// any one host.
const (
	maxConns        = 64
	maxConnsPerHost = 8
)

// ServePeers accepts connections from other devices on addr.
func (n *Node) ServePeers(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.boundAddr = ln.Addr().String()
	n.mu.Unlock()
	go n.acceptPeers(ln)
	return ln, nil
}

func (n *Node) acceptPeers(ln net.Listener) {
	var gate connGate
	for {
		c, err := ln.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			// Usually out of file descriptors, until connections close.
			time.Sleep(100 * time.Millisecond)
			continue
		}
		host := hostOf(c.RemoteAddr())
		if !n.peerHostAllowed(host) || !gate.enter(host) {
			_ = c.Close()
			continue
		}
		go func() {
			defer gate.leave(host)
			defer c.Close()
			_ = n.handleConn(c)
		}()
	}
}

func hostOf(a net.Addr) netip.Addr {
	if ta, ok := a.(*net.TCPAddr); ok {
		return ta.AddrPort().Addr().Unmap().WithZone("")
	}
	ap, _ := netip.ParseAddrPort(a.String())
	return ap.Addr().Unmap().WithZone("")
}

// connGate bounds concurrent connections, so devices that never pair
// can't hold all of the listener's resources.
type connGate struct {
	mu    sync.Mutex
	total int
	hosts map[netip.Addr]int
}

func (g *connGate) enter(host netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= maxConns || g.hosts[host] >= maxConnsPerHost {
		return false
	}
	if g.hosts == nil {
		g.hosts = map[netip.Addr]int{}
	}
	g.total++
	g.hosts[host]++
	return true
}

func (g *connGate) leave(host netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total--
	g.hosts[host]--
	if g.hosts[host] == 0 {
		delete(g.hosts, host)
	}
}

// handleConn serves one connection from another device: pairing with this
// device's offer, or, for a paired device, a sync session for one library.
func (n *Node) handleConn(raw net.Conn) error {
	_ = raw.SetDeadline(time.Now().Add(handshakeTimeout))
	c := tls.Server(raw, n.serverTLS())
	defer c.Close()
	if err := c.Handshake(); err != nil {
		return err
	}
	pub, err := transport.PeerKey(c.ConnectionState())
	if err != nil {
		return err
	}
	msg, err := transport.ReadMsgLimit(c, transport.MaxPreAuthFrame)
	if err != nil {
		return err
	}
	switch msg.Kind {
	case transport.KindPairJoin:
		return n.handlePairJoin(c, pub, msg)
	case transport.KindHello:
		p, ok := n.peerByKey(pub)
		if !ok {
			return refuse(c, "unknown device; pair first")
		}
		return n.handleHello(active{c, n.idleTimeout}, p, msg)
	default:
		return refuse(c, "expected hello or pair-join")
	}
}

// active is a connection whose deadline moves on whenever data does, so
// a session lasts as long as the other device keeps responding.
type active struct {
	net.Conn
	idle time.Duration
}

// activeWrite bounds each write, so a large write over a slow network
// that's still moving doesn't run out its deadline.
const activeWrite = 64 << 10

func (c active) Read(p []byte) (int, error) {
	_ = c.Conn.SetDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c active) Write(p []byte) (int, error) {
	var n int
	for len(p) > 0 {
		_ = c.Conn.SetDeadline(time.Now().Add(c.idle))
		k, err := c.Conn.Write(p[:min(len(p), activeWrite)])
		n += k
		if err != nil {
			return n, err
		}
		p = p[k:]
	}
	return n, nil
}

func refuse(c net.Conn, reason string) error {
	return transport.WriteMsg(c, transport.Msg{Kind: transport.KindError, Error: reason})
}

// maxBody bounds the indexes and want lists this device sends and reads.
// Tests lower it.
var maxBody int64 = transport.MaxBody

// encodeBody encodes v as the body of a message of kind, if another device
// will read it.
func encodeBody(kind transport.Kind, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBody {
		return nil, fmt.Errorf("the %s is %d bytes, more than the %d another device reads", kind, len(raw), maxBody)
	}
	return raw, nil
}

// writeWant asks for the blobs hashes names.
func writeWant(w io.Writer, hashes []string) error {
	raw, err := encodeBody(transport.KindWant, hashes)
	if err != nil {
		return err
	}
	return transport.WriteBody(w, transport.Msg{Kind: transport.KindWant}, raw)
}

// readBody reads a message of kind want and decodes its body into v.
func readBody(r io.Reader, want transport.Kind, v any) error {
	m, body, err := transport.ReadBody(r, maxBody)
	if err != nil {
		return err
	}
	if m.Kind != want {
		return fmt.Errorf("engine: expected %s, got %.40q: %s", want, m.Kind, peerText(m.Error))
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("engine: the %s: %w", want, err)
	}
	return nil
}

func (n *Node) handlePairJoin(c *tls.Conn, pub ed25519.PublicKey, msg transport.Msg) error {
	binding, err := transport.Binding(c.ConnectionState())
	if err != nil {
		return err
	}
	p := config.Peer{
		DeviceID:  identity.IDFromPublic(pub),
		Name:      cleanName(msg.Name),
		PublicKey: identity.EncodePublic(pub),
		PairedAt:  time.Now().UTC(),
		Addresses: []string{peerDialAddr(c.RemoteAddr(), msg.ListenPort)},
	}
	offer, err := n.acceptJoin(p, binding, msg.Proof)
	if errors.Is(err, ErrPairingRejected) {
		return refuse(c, "the pairing code or token is wrong, or the offer was used or expired")
	}
	if err != nil {
		return refuse(c, "pairing not saved")
	}
	proof, err := pairing.Proof(offer.Code, offer.Token, pairing.RoleAccept, binding)
	if err != nil {
		return err
	}
	return transport.WriteMsg(c, transport.Msg{Kind: transport.KindPairOK, Name: n.Device.Name, ListenPort: n.listenPort(), Proof: proof})
}

func (n *Node) handleHello(c net.Conn, p config.Peer, msg transport.Msg) (err error) {
	switch err := n.shared(msg.FolderID, p.DeviceID); {
	case errors.Is(err, ErrNotShared):
		n.Log.Info("refused a library that isn't shared with the device", "library", peerText(msg.FolderID), "device", p.Name)
		return refuse(c, "library not shared with you")
	case err != nil:
		return refuse(c, "library paused")
	}
	var gen uint64
	defer func() { n.recordSync(p.DeviceID, msg.FolderID, gen, err) }()
	defer n.tasks.begin(Task{Library: msg.FolderID, Doing: Syncing, DeviceID: p.DeviceID})()
	n.peerSeen(p.DeviceID, peerDialAddr(c.RemoteAddr(), msg.ListenPort))
	if err := transport.WriteMsg(c, transport.Msg{Kind: transport.KindHelloOK}); err != nil {
		return err
	}
	var local *reconcile.Index
	if local, gen, err = n.exportIndex(msg.FolderID); err != nil {
		_ = refuse(c, err.Error())
		return err
	}
	raw, err := encodeBody(transport.KindIndex, local)
	if err != nil {
		_ = refuse(c, err.Error())
		return err
	}
	if err := transport.WriteBody(c, transport.Msg{Kind: transport.KindIndex, FolderID: msg.FolderID}, raw); err != nil {
		return err
	}
	var theirs reconcile.Index
	if err := readBody(c, transport.KindIndex, &theirs); err != nil {
		return err
	}
	remote, _, err := checkRemote(&theirs, msg.FolderID)
	if err != nil {
		_ = refuse(c, err.Error())
		return err
	}

	// Client wants first, then we want. Avoids deadlock.
	if err := n.serveWants(c, local); err != nil {
		return err
	}
	hashes, release := n.Wants(local, remote)
	defer release()
	if err := writeWant(c, hashes); err != nil {
		return err
	}
	if err := n.recvBlobs(c, hashes, remote); err != nil {
		return err
	}
	// The other device applies what it received meanwhile, then waits to
	// hear how this one did.
	if err := n.PullFrom(remote, n.GetBlob); err != nil {
		_ = refuse(c, "couldn't apply the changes")
		return err
	}
	return transport.WriteMsg(c, transport.Msg{Kind: transport.KindDone})
}

// serveWants sends the content the peer asks for, but only content that the
// index we sent it references. The store holds every library's blobs.
func (n *Node) serveWants(c io.ReadWriter, sent *reconcile.Index) error {
	var want []string
	if err := readBody(c, transport.KindWant, &want); err != nil {
		return err
	}
	allowed := referenced(sent)
	if len(want) > len(allowed) {
		return fmt.Errorf("engine: peer asked for %d blobs, the index references %d", len(want), len(allowed))
	}
	served := make(map[string]bool, len(want))
	for _, h := range want {
		if _, ok := allowed[h]; !ok || served[h] {
			return fmt.Errorf("engine: peer asked for content outside library %s", sent.FolderID)
		}
		served[h] = true
		if err := n.sendBlob(c, h); err != nil {
			return err
		}
	}
	return nil
}

// sendBlob sends a blob from the store. A blob found to be corrupt is
// removed, so the next scan stores the file's content again.
func (n *Node) sendBlob(w io.Writer, hash string) error {
	src, size, err := n.Store.Open(hash)
	if err != nil {
		return err
	}
	err = transport.WriteBlob(w, hash, size, src)
	_ = src.Close()
	if errors.Is(err, blob.ErrCorrupt) {
		_ = n.Store.Remove(hash)
	}
	return err
}

// referenced returns the blobs idx references, each with the most bytes
// it can hold: a file's content is its size, no part of a package is
// bigger than the package, and a manifest is at most maxManifest.
func referenced(idx *reconcile.Index) map[string]int64 {
	out := map[string]int64{}
	allow := func(hash string, size int64) {
		if cur, ok := out[hash]; !ok || size > cur {
			out[hash] = size
		}
	}
	for _, e := range idx.Entries {
		if e.Deleted || e.Incomplete || e.Hash == "" {
			continue
		}
		if e.Kind != reconcile.KindPackage {
			allow(e.Hash, e.Size)
			continue
		}
		allow(e.Hash, maxManifest)
		for _, p := range e.Parts {
			allow(p, e.Size)
		}
	}
	return out
}

// recvBlobs reads one blob for each of hashes. A blob is stored only if
// it's one we asked for, it's no bigger than remote allows, there's room
// for it, and its content matches its hash.
func (n *Node) recvBlobs(r io.Reader, hashes []string, remote *reconcile.Index) error {
	limits := referenced(remote)
	pending := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		pending[h] = true
	}
	for range hashes {
		hash, size, content, err := transport.ReadBlob(r)
		if err != nil {
			return err
		}
		if blob.ValidateHash(hash) != nil || !pending[hash] {
			return fmt.Errorf("engine: peer sent a blob that wasn't requested")
		}
		delete(pending, hash)
		if limit := limits[hash]; size > limit {
			return fmt.Errorf("engine: peer sent %d bytes for %s, which its index allows %d", size, hash, limit)
		}
		if err := n.roomFor(size); err != nil {
			return err
		}
		if _, _, err := n.Store.PutReader(blob.Verify(content, hash, size)); err != nil {
			return fmt.Errorf("engine: receiving %s: %w", hash, err)
		}
	}
	return nil
}

var diskAvailable = atomicfile.DiskAvailable

// roomFor checks that size more bytes fit in the store and leave
// apply.MinFree free.
func (n *Node) roomFor(size int64) error {
	free, err := diskAvailable(n.Store.Root)
	if err != nil {
		return nil
	}
	if need := uint64(size) + apply.MinFree; free < need {
		return fmt.Errorf("engine: not enough free space to receive %d bytes (have %d, and keep %d free)", size, free, apply.MinFree)
	}
	return nil
}

// JoinPeer pairs with the device at addr whose offer has code and token.
// With fingerprint, from the offer's link, it accepts no other key.
// Without it, the proofs both devices exchange still keep out a device in
// the middle, as long as the token stays secret.
func (n *Node) JoinPeer(addr, code, token, fingerprint string) error {
	if fingerprint != "" {
		fp, err := identity.ParseFingerprint(fingerprint)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPairingRejected, err)
		}
		fingerprint = fp
	}
	if _, err := pairing.Proof(code, token, pairing.RoleJoin, nil); err != nil {
		return fmt.Errorf("%w: %v", ErrPairingRejected, err)
	}
	c, err := transport.Dial(addr, dialTimeout, transport.Config(n.cert, func(pub ed25519.PublicKey) error {
		if fingerprint != "" && identity.Fingerprint(pub) != fingerprint {
			return fmt.Errorf("%w: the device at %s isn't the one that made the offer", ErrPairingRejected, addr)
		}
		return nil
	}))
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(handshakeTimeout))
	cs := c.ConnectionState()
	pub, err := transport.PeerKey(cs)
	if err != nil {
		return err
	}
	binding, err := transport.Binding(cs)
	if err != nil {
		return err
	}
	proof, err := pairing.Proof(code, token, pairing.RoleJoin, binding)
	if err != nil {
		return err
	}
	if err := transport.WriteMsg(c, transport.Msg{Kind: transport.KindPairJoin, Name: n.Device.Name, ListenPort: n.listenPort(), Proof: proof}); err != nil {
		return err
	}
	resp, err := transport.ReadMsgLimit(c, transport.MaxPreAuthFrame)
	if err != nil {
		return err
	}
	if resp.Kind != transport.KindPairOK {
		return fmt.Errorf("%w: %s", ErrPairingRejected, peerText(resp.Error))
	}
	if !pairing.CheckProof(code, token, pairing.RoleAccept, binding, resp.Proof) {
		return fmt.Errorf("%w: the device at %s doesn't know the offer's token", ErrPairingRejected, addr)
	}
	p := config.Peer{
		DeviceID:  identity.IDFromPublic(pub),
		Name:      cleanName(resp.Name),
		PublicKey: identity.EncodePublic(pub),
		PairedAt:  time.Now().UTC(),
		Addresses: []string{addr},
	}
	if err := n.addPeer(p); err != nil {
		return err
	}
	n.Log.Info("paired", "device", p.Name, "id", p.DeviceID)
	return nil
}

// SyncWith syncs one library with the paired device at addr.
func (n *Node) SyncWith(addr, folderID string) error {
	c, err := transport.Dial(addr, dialTimeout, transport.Config(n.cert, n.anyPeer))
	if err != nil {
		return err
	}
	return n.syncOn(c, addr, folderID)
}

// syncOn runs a sync session for one library on c, a connection to a
// paired device at addr, and closes c.
func (n *Node) syncOn(c *tls.Conn, addr, folderID string) error {
	defer c.Close()
	pub, err := transport.PeerKey(c.ConnectionState())
	if err != nil {
		return err
	}
	p, ok := n.peerByKey(pub)
	if !ok {
		return ErrNotPaired
	}
	if err := n.shared(folderID, p.DeviceID); err != nil {
		return fmt.Errorf("%s with %s: %w", folderID, p.Name, err)
	}
	return n.hello(active{c, n.idleTimeout}, p, addr, folderID)
}

// hello is the side of a sync session that starts it, on c, a connection
// to p at addr.
func (n *Node) hello(c net.Conn, p config.Peer, addr, folderID string) (err error) {
	var gen uint64
	defer func() { n.recordSync(p.DeviceID, folderID, gen, err) }()
	defer n.tasks.begin(Task{Library: folderID, Doing: Syncing, DeviceID: p.DeviceID})()
	if err := transport.WriteMsg(c, transport.Msg{Kind: transport.KindHello, FolderID: folderID, ListenPort: n.listenPort()}); err != nil {
		return err
	}
	helloOK, err := transport.ReadMsg(c)
	if err != nil {
		return err
	}
	if helloOK.Kind != transport.KindHelloOK {
		return fmt.Errorf("%w: %s", ErrRefused, peerText(helloOK.Error))
	}
	n.peerSeen(p.DeviceID, addr)
	var theirs reconcile.Index
	if err := readBody(c, transport.KindIndex, &theirs); err != nil {
		return err
	}
	remote, _, err := checkRemote(&theirs, folderID)
	if err != nil {
		return err
	}
	var local *reconcile.Index
	if local, gen, err = n.exportIndex(folderID); err != nil {
		return err
	}
	raw, err := encodeBody(transport.KindIndex, local)
	if err != nil {
		_ = refuse(c, err.Error())
		return err
	}
	if err := transport.WriteBody(c, transport.Msg{Kind: transport.KindIndex, FolderID: folderID}, raw); err != nil {
		return err
	}

	hashes, release := n.Wants(local, remote)
	defer release()
	if err := writeWant(c, hashes); err != nil {
		return err
	}
	if err := n.recvBlobs(c, hashes, remote); err != nil {
		return err
	}
	if err := n.serveWants(c, local); err != nil {
		return err
	}
	applyErr := n.PullFrom(remote, n.GetBlob)
	done, err := transport.ReadMsg(c)
	switch {
	case err != nil:
	case done.Kind == transport.KindError:
		err = fmt.Errorf("engine: %s %s", p.Name, peerText(done.Error))
	case done.Kind != transport.KindDone:
		err = fmt.Errorf("engine: expected done, got %.40q", done.Kind)
	}
	return errors.Join(applyErr, err)
}

// peerText makes text from another device safe to show: printable, on one
// line, and short.
func peerText(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }), " ")
	if r := []rune(s); len(r) > maxPeerText {
		s = string(r[:maxPeerText]) + "…"
	}
	return s
}

const maxPeerText = 200
