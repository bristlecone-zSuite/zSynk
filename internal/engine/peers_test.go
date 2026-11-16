package engine

import (
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
)

// A library syncs only with the devices on its list, whichever end asks.
func TestLibrariesSyncOnlyWithTheirDevices(t *testing.T) {
	a, b, c := openNode(t, "Mac"), openNode(t, "iPhone"), openNode(t, "iPad")
	addrA, addrC := listen(t, a), listen(t, c)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	joinOffer(t, b, addrC, offer(t, c), c.Fingerprint())
	must(t, Pair(a, c))

	dirB := library(t, b, "notes", c.Device.DeviceID)
	dirA, dirC := library(t, a, "notes"), library(t, c, "notes")
	writeFile(t, dirB, "plan.md", "for the iPad")
	must(t, b.SyncAllPeers())
	wantFile(t, dirC, "plan.md", "for the iPad")
	wantGone(t, dirA, "plan.md")
	if err := b.SyncWith(addrA, "notes"); !errors.Is(err, ErrNotShared) {
		t.Fatalf("B synced notes with a device that isn't on its list: %v", err)
	}
	wantGone(t, dirA, "plan.md")

	// A answers a library that isn't shared with B as it does one that
	// doesn't exist, so B can't find out which libraries A has.
	writeFile(t, library(t, a, "photos", c.Device.DeviceID), "a.jpg", "private")
	dirPhotos := library(t, b, "photos")
	library(t, b, "missing")
	unshared, missing := b.SyncWith(addrA, "photos"), b.SyncWith(addrA, "missing")
	if !errors.Is(unshared, ErrRefused) || !strings.Contains(unshared.Error(), "not shared") || unshared.Error() != missing.Error() {
		t.Fatalf("A answered an unshared library with %v and a missing one with %v", unshared, missing)
	}
	wantGone(t, dirPhotos, "a.jpg")
}

func TestPausedLibrariesDontSync(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	writeFile(t, dirA, "todo.md", "paused")
	pause(t, a, "notes", true)
	if err := b.SyncWith(addrA, "notes"); !errors.Is(err, ErrRefused) {
		t.Fatalf("A synced a paused library: %v", err)
	}
	pause(t, a, "notes", false)
	pause(t, b, "notes", true)
	if err := b.SyncWith(addrA, "notes"); !errors.Is(err, ErrPaused) {
		t.Fatalf("B synced a paused library: %v", err)
	}
	must(t, b.SyncAllPeers())
	wantGone(t, dirB, "todo.md")
	pause(t, b, "notes", false)
	must(t, b.SyncAllPeers())
	wantFile(t, dirB, "todo.md", "paused")
}

func TestShareWithNamesPairedDevices(t *testing.T) {
	a, b, c := openNode(t, "Mac"), openNode(t, "iPhone"), openNode(t, "iPad")
	must(t, Pair(a, b))
	must(t, Pair(a, c))
	lib := config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes")}
	shares := func() []string {
		got, _ := a.Library("notes")
		return got.ShareWith
	}
	for _, list := range [][]string{{"ABCD-EFGH-JKMN-PQRS"}, {b.Device.DeviceID, ""}, {a.Device.DeviceID}} {
		lib.ShareWith = list
		if err := a.EnableLibrary(lib); !errors.Is(err, ErrInvalidLibrary) {
			t.Fatalf("shared with %q: %v", list, err)
		}
	}

	lib.ShareWith = []string{strings.ToLower(c.Device.DeviceID), b.Device.DeviceID, " " + c.Device.DeviceID + " "}
	must(t, a.EnableLibrary(lib))
	want := []string{b.Device.DeviceID, c.Device.DeviceID}
	slices.Sort(want)
	if !slices.Equal(shares(), want) {
		t.Fatalf("shared with %q; want %q", shares(), want)
	}
	lib.ShareWith, lib.Label = nil, "Renamed"
	must(t, a.EnableLibrary(lib))
	if !slices.Equal(shares(), want) {
		t.Fatalf("enabling a library again without a list changed it to %q", shares())
	}

	// A device that leaves the paired list can stay on a library's list,
	// but not join one.
	locked(a, func() {
		a.Config.Peers = slices.DeleteFunc(a.Config.Peers, func(p config.Peer) bool { return p.DeviceID == b.Device.DeviceID })
	})
	lib.ShareWith = []string{b.Device.DeviceID}
	must(t, a.EnableLibrary(lib))
	lib.ShareWith = []string{b.Device.DeviceID, "ABCD-EFGH-JKMN-PQRS"}
	if err := a.EnableLibrary(lib); !errors.Is(err, ErrInvalidLibrary) {
		t.Fatalf("added a device that isn't paired to a library's list: %v", err)
	}
	other := lib
	other.ID, other.Path = "other", filepath.Join(t.TempDir(), "other")
	if err := a.EnableLibrary(other); !errors.Is(err, ErrInvalidLibrary) {
		t.Fatalf("shared a new library with a device that isn't paired: %v", err)
	}

	lib.ShareWith = []string{}
	must(t, a.EnableLibrary(lib))
	if got := shares(); len(got) != 0 {
		t.Fatalf("an empty list kept %q", got)
	}
}

// Each device learns where the other is from the sessions between them,
// and tries the address that worked last first.
func TestPeerAddressesFollowTheDevice(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrB := listen(t, b)
	ln, err := a.ServePeers("127.0.0.1:0")
	must(t, err)
	oldA := ln.Addr().String()
	joinOffer(t, b, oldA, offer(t, a), a.Fingerprint())
	library(t, a, "notes")
	library(t, b, "notes")
	if p, _ := a.Peer(b.Device.DeviceID); !slices.Equal(p.Addresses, []string{addrB}) {
		t.Fatalf("A has B at %v; B listens on %s", p.Addresses, addrB)
	}

	must(t, ln.Close())
	newA := listen(t, a)
	if err := b.SyncAllPeers(); err == nil {
		t.Fatal("B synced with A where A no longer listens")
	}
	start := time.Now()
	must(t, a.SyncAllPeers())
	p, _ := b.Peer(a.Device.DeviceID)
	if !slices.Equal(p.Addresses, []string{newA, oldA}) || p.LastSeen.Before(start) {
		t.Fatalf("after A connected from its new port, B has A at %v, seen %v", p.Addresses, p.LastSeen)
	}

	locked(b, func() { b.Config.PeerByID(a.Device.DeviceID).Addresses = []string{oldA, newA} })
	start = time.Now()
	must(t, b.SyncAllPeers())
	p, _ = b.Peer(a.Device.DeviceID)
	if !slices.Equal(p.Addresses, []string{newA, oldA}) || p.LastSeen.Before(start) {
		t.Fatalf("B has A at %v, seen %v", p.Addresses, p.LastSeen)
	}
	if p, _ := a.Peer(b.Device.DeviceID); p.LastSeen.Before(start) {
		t.Fatalf("A last saw B at %v", p.LastSeen)
	}
}

func TestPeerSeenKeepsRecentAddresses(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	id := b.Device.DeviceID
	start := time.Now()
	for _, last := range []string{"1", "2", "3", "4", "5", "3", "3"} {
		a.peerSeen(id, "10.0.0."+last+":7420")
	}
	a.peerSeen("ABCD-EFGH-JKMN-PQRS", "10.0.0.9:7420")
	want := []string{"10.0.0.3:7420", "10.0.0.5:7420", "10.0.0.4:7420", "10.0.0.2:7420"}
	if p, _ := a.Peer(id); !slices.Equal(p.Addresses, want) || p.LastSeen.Before(start) {
		t.Fatalf("%v, seen %v; want %v", p.Addresses, p.LastSeen, want)
	}
	if peers := a.Status().Peers; len(peers) != 1 {
		t.Fatalf("seeing an unknown device paired it: %+v", peers)
	}
	must(t, a.Close())
	if p, _ := openNodeAt(t, a.DataDir, "Mac").Peer(id); !slices.Equal(p.Addresses, want) {
		t.Fatalf("after a restart, %v", p.Addresses)
	}
}

func TestOnLocalNetwork(t *testing.T) {
	lan := func() []netip.Prefix {
		return []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8:1::/48")}
	}
	for addr, want := range map[string]bool{
		"127.0.0.1":            true,
		"::1":                  true,
		"10.1.2.3":             true,
		"172.16.5.4":           true,
		"192.168.1.20":         true,
		"100.100.1.1":          true,
		"169.254.10.10":        true,
		"fe80::1%en0":          true,
		"fd12:3456::1":         true,
		"::ffff:192.168.1.20":  true,
		"::ffff:100.100.1.1":   true,
		"203.0.113.7":          true,
		"::ffff:203.0.113.7":   true,
		"2001:db8:1::5":        true,
		"2001:db8:1::5%en0":    true,
		"8.8.8.8":              false,
		"172.32.0.1":           false,
		"100.128.0.1":          false,
		"198.51.100.1":         false,
		"2001:4860:4860::8888": false,
	} {
		if got := onLocalNetwork(netip.MustParseAddr(addr), lan); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}

func TestLANAddresses(t *testing.T) {
	ipNet := func(cidr string) net.Addr {
		ip, n, err := net.ParseCIDR(cidr)
		must(t, err)
		n.IP = ip
		return n
	}
	got := lanAddresses([]net.Addr{
		ipNet("fd12:3456::5/64"),
		ipNet("192.168.1.5/24"),
		ipNet("8.8.8.8/24"),
		ipNet("2001:db8::1/64"),
		ipNet("fe80::1/64"),
		ipNet("127.0.0.1/8"),
		ipNet("100.100.1.1/10"),
		&net.IPAddr{IP: net.ParseIP("10.0.0.9")},
	}, "7420")
	want := []string{"192.168.1.5:7420", "100.100.1.1:7420", "[fd12:3456::5]:7420"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v; want %v", got, want)
	}
}

func TestSubnetsOf(t *testing.T) {
	got := subnetsOf([]net.Addr{
		&net.IPNet{IP: net.ParseIP("192.168.1.5").To4(), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("10.1.2.3"), Mask: net.CIDRMask(120, 128)},
		&net.IPNet{IP: net.ParseIP("2001:db8:1::5"), Mask: net.CIDRMask(48, 128)},
		&net.IPNet{IP: net.ParseIP("203.0.113.9").To4(), Mask: net.IPMask{255, 0, 255, 0}},
		&net.IPNet{IP: net.ParseIP("198.51.100.9").To4(), Mask: net.CIDRMask(0, 32)},
		&net.IPAddr{IP: net.ParseIP("172.16.0.1")},
	})
	want := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("10.1.2.0/24"),
		netip.MustParsePrefix("2001:db8:1::/48"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%v; want %v", got, want)
	}
}

// A device on the internet doesn't get as far as the handshake unless the
// config allows it.
func TestInternetPeersNeedTheConfig(t *testing.T) {
	a := openNode(t, "Mac")
	far := netip.MustParseAddr("198.51.100.7")
	if onLocalNetwork(far, localPrefixes) {
		t.Skip("this machine is on 198.51.100.0/24")
	}
	reaches := func() bool {
		c := newFarConn(far)
		defer c.Close()
		a.acceptPeers(&listenerOf{c})
		select {
		case <-c.closed:
			return false
		case <-time.After(300 * time.Millisecond):
			return true
		}
	}
	if reaches() {
		t.Fatal("a device on the internet reached the handshake")
	}
	locked(a, func() { a.Config.AllowInternetPeers = true })
	if !reaches() {
		t.Fatal("allowInternetPeers didn't let a device on the internet connect")
	}
}

func TestConnGate(t *testing.T) {
	var g connGate
	one, other := netip.MustParseAddr("192.168.1.2"), netip.MustParseAddr("192.168.1.3")
	for range maxConnsPerHost {
		if !g.enter(one) {
			t.Fatal("refused a connection within the host's limit")
		}
	}
	if g.enter(one) {
		t.Fatalf("one host got more than %d connections", maxConnsPerHost)
	}
	if !g.enter(other) {
		t.Fatal("one host's connections kept out another's")
	}
	g.leave(other)
	g.leave(one)
	if !g.enter(one) {
		t.Fatal("a closed connection still counts")
	}
	if len(g.hosts) != 1 {
		t.Fatalf("hosts with no connections stay in the gate: %v", g.hosts)
	}
	for i := 0; g.enter(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)})); i++ {
		if i > maxConns {
			t.Fatal("no limit on all connections")
		}
	}
	if g.total != maxConns {
		t.Fatalf("stopped at %d connections; want %d", g.total, maxConns)
	}
}

func TestPeerDialAddr(t *testing.T) {
	for _, c := range []struct{ remote, port, want string }{
		{"192.168.1.5:50123", "7425", "192.168.1.5:7425"},
		{"192.168.1.5:50123", "", "192.168.1.5:7420"},
		{"192.168.1.5:50123", "0", "192.168.1.5:7420"},
		{"192.168.1.5:50123", "65536", "192.168.1.5:7420"},
		{"192.168.1.5:50123", "+80", "192.168.1.5:7420"},
		{"192.168.1.5:50123", "80/../x", "192.168.1.5:7420"},
		{"[fe80::1%en0]:50123", "7425", "[fe80::1%en0]:7425"},
	} {
		remote := net.TCPAddrFromAddrPort(netip.MustParseAddrPort(c.remote))
		if got := peerDialAddr(remote, c.port); got != c.want {
			t.Errorf("from %s, port %q: %s; want %s", c.remote, c.port, got, c.want)
		}
	}
}

// An offer lists where this device listens: the address it's bound to, or
// with a wildcard, its addresses on local networks.
func TestOfferAddresses(t *testing.T) {
	a := openNode(t, "Mac")
	locked(a, func() { a.Config.Listen = "0.0.0.0:7499" })
	if got := a.listenPort(); got != "7499" {
		t.Fatalf("before listening, port %s; the config says 7499", got)
	}
	addr := listen(t, a)
	if got := a.LANAddresses(); !slices.Equal(got, []string{addr}) {
		t.Fatalf("bound to %s, the offer lists %v", addr, got)
	}
	ln, err := a.ServePeers("0.0.0.0:0")
	must(t, err)
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if got := a.listenPort(); got != port {
		t.Fatalf("port %s; listening on %s", got, port)
	}
	v6 := false
	for _, s := range a.LANAddresses() {
		ap := netip.MustParseAddrPort(s)
		if ip := ap.Addr(); strconv.Itoa(int(ap.Port())) != port || !ip.IsPrivate() && !sharedSpace.Contains(ip) || ip.Is4() && v6 {
			t.Errorf("the offer lists %s", s)
		}
		v6 = v6 || ap.Addr().Is6()
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"Studio Mac":             "Studio Mac",
		"  Two\n\tlines  ":       "Two lines",
		"evil\x1b[31mred":        "evil [31mred",
		"\u202eelif.exe":         "elif.exe",
		"":                       "Unnamed device",
		"\x00\x01":               "Unnamed device",
		strings.Repeat("é", 70):  strings.Repeat("é", maxNameLen),
		strings.Repeat("ab", 40): strings.Repeat("ab", maxNameLen/2),
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q; want %q", in, got, want)
		}
	}
}

func pause(t *testing.T, n *Node, id string, paused bool) {
	t.Helper()
	lib, ok := n.Library(id)
	if !ok {
		t.Fatalf("no library %s", id)
	}
	lib.Paused = paused
	must(t, n.EnableLibrary(lib))
}

// locked changes n's state under its lock. The lock is released even if
// change fails, so the test's cleanup can still close n.
func locked(n *Node, change func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	change()
}

// farConn is one end of a pipe that says it comes from addr.
type farConn struct {
	net.Conn
	other  net.Conn
	addr   netip.Addr
	closed chan struct{}
	once   sync.Once
}

func newFarConn(addr netip.Addr) *farConn {
	c, other := net.Pipe()
	return &farConn{Conn: c, other: other, addr: addr, closed: make(chan struct{})}
}

func (c *farConn) RemoteAddr() net.Addr {
	return net.TCPAddrFromAddrPort(netip.AddrPortFrom(c.addr, 50000))
}

func (c *farConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		_ = c.other.Close()
	})
	return c.Conn.Close()
}

// listenerOf accepts its connections in turn, then reports it's closed.
type listenerOf []net.Conn

func (l *listenerOf) Accept() (net.Conn, error) {
	if len(*l) == 0 {
		return nil, net.ErrClosed
	}
	c := (*l)[0]
	*l = (*l)[1:]
	return c, nil
}

func (l *listenerOf) Close() error { return nil }

func (l *listenerOf) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
