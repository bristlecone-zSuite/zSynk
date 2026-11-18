package engine

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
	"github.com/bristlecone-zSuite/zSynk/relay"
)

// maxPeerAddrs bounds the addresses kept for each paired device.
const maxPeerAddrs = 4

// ArmPairing makes code and token the offer one device may pair with, for
// pairing.DefaultTTL, replacing any earlier offer. The offer is saved, so a
// `synk serve` started later accepts it.
func (n *Node) ArmPairing(code, token string) error {
	p := &pairing.Pending{Code: code, Token: token, CreatedAt: time.Now().UTC()}
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := pairing.SavePending(n.DataDir, p); err != nil {
		return err
	}
	n.offer = p
	return nil
}

// PairingOffer returns the offer waiting for a device to pair, if there is
// one.
func (n *Node) PairingOffer() (pairing.Pending, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.offer == nil || time.Now().After(n.offer.ExpiresAt) {
		return pairing.Pending{}, false
	}
	return *n.offer, true
}

// acceptJoin pairs with p if proof shows that the device on the connection
// with binding knows the waiting offer. The offer is used up in the same
// step, so only one device can pair with it.
func (n *Node) acceptJoin(p config.Peer, binding []byte, proof string) (pairing.Pending, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	o := n.offer
	if o == nil || time.Now().After(o.ExpiresAt) || !pairing.CheckProof(o.Code, o.Token, pairing.RoleJoin, binding, proof) {
		return pairing.Pending{}, ErrPairingRejected
	}
	before := slices.Clone(n.Config.Peers)
	n.Config.UpsertPeer(p)
	if err := n.saveConfigLocked(); err != nil {
		n.Config.Peers = before
		return pairing.Pending{}, err
	}
	n.offer = nil
	_ = pairing.ClearPending(n.DataDir)
	n.Log.Info("paired", "device", p.Name, "id", p.DeviceID)
	return *o, nil
}

// Fingerprint is this device's key fingerprint, which its offers carry.
func (n *Node) Fingerprint() string {
	pub, err := n.Device.Public()
	if err != nil {
		return ""
	}
	return identity.Fingerprint(pub)
}

// LANAddresses returns where on the local network this device listens for
// peers, for an offer to carry.
func (n *Node) LANAddresses() []string {
	n.mu.Lock()
	bound := n.boundAddr
	n.mu.Unlock()
	port := n.listenPort()
	if ap, err := netip.ParseAddrPort(bound); err == nil && !ap.Addr().IsUnspecified() {
		return []string{bound}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return lanAddresses(addrs, port)
}

// lanAddresses lists the private and shared-space addresses among an
// interface's addrs, at port. IPv4 comes first: it's likelier to reach
// this device from another.
func lanAddresses(addrs []net.Addr, port string) []string {
	var v4, v6 []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.IsPrivate() && !sharedSpace.Contains(ip) {
			continue
		}
		if ip.Is4() {
			v4 = append(v4, net.JoinHostPort(ip.String(), port))
		} else {
			v6 = append(v6, net.JoinHostPort(ip.String(), port))
		}
	}
	return append(v4, v6...)
}

// sharedSpace is RFC 6598 shared address space, used by carrier-grade NAT
// and by Tailscale. It isn't reachable from the internet.
var sharedSpace = netip.MustParsePrefix("100.64.0.0/10")

// peerHostAllowed reports whether a device at ip may connect: one on a
// network this device is on, or anywhere if the config allows that.
func (n *Node) peerHostAllowed(ip netip.Addr) bool {
	n.mu.Lock()
	anywhere := n.Config.AllowInternetPeers
	n.mu.Unlock()
	return anywhere || onLocalNetwork(ip, localPrefixes)
}

// onLocalNetwork reports whether ip is loopback, link-local, private,
// shared address space, or inside a subnet of one of this device's
// interfaces, rather than somewhere on the internet.
func onLocalNetwork(ip netip.Addr, prefixes func() []netip.Prefix) bool {
	ip = ip.Unmap().WithZone("")
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || sharedSpace.Contains(ip) {
		return true
	}
	for _, p := range prefixes() {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func localPrefixes() []netip.Prefix {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	return subnetsOf(addrs)
}

// subnetsOf returns the subnets of an interface's addrs. An address
// without a proper mask has none, rather than one that takes in the
// internet.
func subnetsOf(addrs []net.Addr) []netip.Prefix {
	var out []netip.Prefix
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ones, bits := ipn.Mask.Size()
		ip = ip.Unmap()
		if ip.Is4() && bits == 128 {
			ones -= 96
		}
		if p := netip.PrefixFrom(ip, ones).Masked(); p.IsValid() && ones > 0 {
			out = append(out, p)
		}
	}
	return out
}

func (n *Node) listenPort() string {
	n.mu.Lock()
	bound := n.boundAddr
	cfg := n.Config.Listen
	n.mu.Unlock()
	if _, port, err := net.SplitHostPort(bound); err == nil && port != "" && port != "0" {
		return port
	}
	if _, port, err := net.SplitHostPort(cfg); err == nil && port != "" && port != "0" {
		return port
	}
	return "7420"
}

func (n *Node) BoundAddr() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.boundAddr
}

// peerDialAddr is where a device that connected from remote listens: the
// same host, at the port it says it listens on.
func peerDialAddr(remote net.Addr, listenPort string) string {
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		host = remote.String()
	}
	if p, err := strconv.ParseUint(listenPort, 10, 16); err != nil || p == 0 {
		listenPort = "7420"
	}
	return net.JoinHostPort(host, listenPort)
}

// peerSeen records that the paired device answered at addr just now. addr
// moves to the front of its addresses, so it's tried first next time.
func (n *Node) peerSeen(id, addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.Config.PeerByID(id)
	if p == nil {
		return
	}
	p.LastSeen = time.Now().UTC()
	if addr == "" || (len(p.Addresses) > 0 && p.Addresses[0] == addr) {
		return
	}
	addrs := append([]string{addr}, slices.DeleteFunc(slices.Clone(p.Addresses), func(a string) bool { return a == addr })...)
	p.Addresses = addrs[:min(len(addrs), maxPeerAddrs)]
	_ = n.saveConfigLocked()
}

func sharesWith(l config.Library, deviceID string) bool {
	return len(l.ShareWith) == 0 || slices.Contains(l.ShareWith, deviceID)
}

// shared returns nil if library id syncs with the device now. A library
// that doesn't exist reads as one that isn't shared, so peers can't probe
// for library IDs.
func (n *Node) shared(id, deviceID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	l := n.Config.LibraryByID(id)
	if l == nil || !sharesWith(*l, deviceID) {
		return ErrNotShared
	}
	if l.Paused {
		return ErrPaused
	}
	return nil
}

// resync is how often a library syncs with a device while it doesn't
// change here. The other device syncs its own changes as it makes them;
// this catches what those sessions missed.
const resync = time.Minute

// syncPair is a library and a device it syncs with.
type syncPair struct{ device, library string }

// syncMark is how a library's sessions with a device went. at is when the
// last one succeeded, and gen how many times the index here had changed
// when it was sent; changes the session brought make the library due
// again, so the next session confirms both ends match. failures counts
// the sessions that failed since, and err and failedAt are the last one's.
type syncMark struct {
	gen      uint64
	at       time.Time
	failures int
	err      string
	failedAt time.Time
}

// After a session fails, the library waits retryMin to sync with the
// device again, and twice as long after each further failure, up to
// retryMax.
const (
	retryMin = 5 * time.Second
	retryMax = 5 * time.Minute
)

func retryAfter(failures int) time.Duration {
	d := retryMin
	for i := 1; i < failures && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// recordSync notes how a session for a library with a device ended. gen
// is how many times the index here had changed when it was sent.
func (n *Node) recordSync(deviceID, libID string, gen uint64, err error) {
	n.mu.Lock()
	k := syncPair{deviceID, libID}
	m := n.synced[k]
	name := deviceID
	if p := n.Config.PeerByID(deviceID); p != nil {
		name = p.Name
	}
	if err == nil {
		n.synced[k] = syncMark{gen: gen, at: time.Now()}
	} else {
		m.failures++
		m.err, m.failedAt = err.Error(), time.Now()
		n.synced[k] = m
	}
	n.mu.Unlock()
	switch {
	case err != nil:
		n.Log.Warn("sync failed", "library", libID, "device", name, "failures", m.failures, "retryIn", retryAfter(m.failures), "err", err)
		n.publish("sync", libID, name, "warning", "syncing with "+name+" failed: "+peerText(err.Error()))
	case m.failures > 0:
		n.Log.Info("syncing again", "library", libID, "device", name, "failures", m.failures)
		n.publish("sync", libID, name, "info", "syncing with "+name+" works again")
	default:
		n.Log.Debug("synced", "library", libID, "device", name)
	}
}

// dueLocked reports whether a library is due to sync with a device: it
// hasn't yet, it changed here since it did, or resync has passed. After a
// failure, it's due once retryAfter has passed.
func (n *Node) dueLocked(deviceID, libID string) bool {
	m, ok := n.synced[syncPair{deviceID, libID}]
	switch {
	case !ok:
		return true
	case m.failures > 0:
		return time.Since(m.failedAt) >= retryAfter(m.failures)
	}
	return m.gen != n.gens[libID] || time.Since(m.at) >= resync
}

// SyncAllPeers syncs each library with each paired device it's shared
// with.
func (n *Node) SyncAllPeers() error {
	return n.syncPeers(false)
}

// SyncChanged is SyncAllPeers for the libraries that are due to sync with
// each device: they changed here since they last synced with it, or
// resync has passed.
func (n *Node) SyncChanged() error {
	return n.syncPeers(true)
}

func (n *Node) syncPeers(onlyDue bool) error {
	n.mu.Lock()
	peers := slices.Clone(n.Config.Peers)
	libs := make([]config.Library, 0, len(n.Config.Libraries))
	for _, l := range n.Config.Libraries {
		libs = append(libs, cloneLibrary(l))
	}
	n.mu.Unlock()
	var first error
	for _, p := range peers {
		for _, lib := range libs {
			if lib.Paused || !sharesWith(lib, p.DeviceID) {
				continue
			}
			n.mu.Lock()
			due := !onlyDue || n.dueLocked(p.DeviceID, lib.ID)
			n.mu.Unlock()
			if !due {
				continue
			}
			if err := n.syncPeer(p.DeviceID, lib.ID); err != nil && first == nil {
				first = fmt.Errorf("%s/%s: %w", p.Name, lib.ID, err)
			}
		}
	}
	return first
}

// syncPeer syncs a library with the paired device at the first of its
// addresses that answers as that device, or, when no address works and a
// relay is configured, through the pair's relay room (decision 0013).
func (n *Node) syncPeer(deviceID, folderID string) error {
	p, ok := n.Peer(deviceID)
	if !ok {
		return ErrNotPaired
	}
	err := fmt.Errorf("engine: no address for %s", p.Name)
	for _, addr := range p.Addresses {
		var c *tls.Conn
		if c, err = transport.Dial(addr, dialTimeout, transport.Config(n.cert, thisPeer(p))); err == nil {
			return n.syncOn(c, addr, folderID)
		}
	}
	if relayAddr := n.relayURL(); relayAddr != "" {
		err = n.relaySync(p, relayAddr, folderID)
	}
	n.recordSync(deviceID, folderID, 0, err)
	return err
}

// relayURL returns the configured relay, or "" when none is set.
func (n *Node) relayURL() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.Config.Relay
}

// relaySync runs one library sync through the pair's relay room. Both
// devices dial the relay — the room exists only while they are both there —
// and the device with the greater ID starts the session, sending hello
// exactly as the dialer of a direct connection does; the other answers on
// the connection the way the listener does. The role is a pure function of
// the two IDs, so exactly one device takes each side (decision 0013).
func (n *Node) relaySync(p config.Peer, relayAddr, folderID string) error {
	conn, err := n.relayConn(p, relayAddr, folderID)
	if err != nil {
		return err
	}
	if n.Device.DeviceID > p.DeviceID {
		c, err := transport.DialConn(conn, relayTimeout, transport.Config(n.cert, thisPeer(p)))
		if err != nil {
			return err
		}
		return n.syncOn(c, "", folderID)
	}
	// The answer's deadline must cover the other device's wait in the
	// room before the splice forms.
	_ = conn.SetDeadline(time.Now().Add(relayAnswerTimeout))
	return n.handleConn(conn, true)
}

// relayConn joins the pair's relay room for the library and returns the
// spliced connection to the peer. The room's secret derives from the two
// public keys (decision 0012), so both devices find the same room without
// any extra exchange, and its name includes the library (decision 0013).
func (n *Node) relayConn(p config.Peer, relayAddr, folderID string) (net.Conn, error) {
	peerKey, err := identity.ParsePublic(p.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("engine: relay: %s: %w", p.Name, err)
	}
	myKey, err := n.Device.Public()
	if err != nil {
		return nil, err
	}
	secret := relay.SecretFromKeys(myKey, peerKey)
	return relay.Dial(relayAddr, "", secret, n.Device.DeviceID, p.DeviceID, folderID, relayTimeout)
}
