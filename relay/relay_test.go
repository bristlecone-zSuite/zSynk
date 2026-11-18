package relay_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/relay"
)

// startServer runs a relay server on a loopback port and returns its
// address.
func startServer(t *testing.T, wait time.Duration) string {
	t.Helper()
	srv := relay.NewServer()
	srv.Wait = wait
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go http.Serve(ln, srv.Handler())
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// readN reads exactly n bytes.
func readN(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

// ---- raw WebSocket client (for protocol-level tests) ----

type rawClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

// rawDial performs the client WebSocket upgrade against addr/room and
// returns a client with full control over frame masking.
func rawDial(t *testing.T, addr, room string) *rawClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	req := "GET /v1/rooms/" + room + " HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	conn.SetDeadline(time.Time{})
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("status line: %v", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 101") {
		t.Fatalf("status: %q", strings.TrimSpace(statusLine))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("header: %v", err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	return &rawClient{t: t, conn: conn, br: br}
}

// send writes one unfragmented frame. mask controls the mask bit: clients
// must mask, and the relay must reject unmasked client frames.
func (c *rawClient) send(opcode byte, payload []byte, mask bool) {
	c.t.Helper()
	if err := c.sendFrame(opcode, payload, mask); err != nil {
		c.t.Fatalf("send frame: %v", err)
	}
}

// sendFrame is send without the fatal: the relay may close the connection
// before the whole frame is written (an oversized frame, for example), and
// the test asserts on what the other peer sees.
func (c *rawClient) sendFrame(opcode byte, payload []byte, mask bool) error {
	c.t.Helper()
	var b1 byte
	n := len(payload)
	var extra []byte
	switch {
	case n <= 125:
		b1 = byte(n)
	case n <= 65535:
		b1 = 126
		extra = make([]byte, 2)
		binary.BigEndian.PutUint16(extra, uint16(n))
	default:
		b1 = 127
		extra = make([]byte, 8)
		binary.BigEndian.PutUint64(extra, uint64(n))
	}
	if mask {
		b1 |= 0x80
	}
	var buf bytes.Buffer
	buf.WriteByte(0x80 | opcode) // FIN set.
	buf.WriteByte(b1)
	buf.Write(extra)
	var key [4]byte
	if mask {
		if _, err := rand.Read(key[:]); err != nil {
			c.t.Fatal(err)
		}
		buf.Write(key[:])
		masked := make([]byte, n)
		for i := range payload {
			masked[i] = payload[i] ^ key[i&3]
		}
		buf.Write(masked)
	} else {
		buf.Write(payload)
	}
	c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(buf.Bytes())
	c.conn.SetDeadline(time.Time{})
	return err
}

// recv reads one frame.
func (c *rawClient) recv() (opcode byte, payload []byte, err error) {
	c.t.Helper()
	c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetDeadline(time.Time{})
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return 0, nil, err
	}
	opcode = head[0] & 0x0f
	masked := head[1]&0x80 != 0
	n := int(head[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint64(ext[:]))
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, key[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= key[i&3]
		}
	}
	return opcode, payload, nil
}

func closeCode(payload []byte) int {
	if len(payload) < 2 {
		return -1
	}
	return int(binary.BigEndian.Uint16(payload[:2]))
}

// upgradeStatus performs a WebSocket upgrade and returns the HTTP status
// code of the response (101 on success).
func upgradeStatus(t *testing.T, addr, room string) int {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	req := "GET /v1/rooms/" + room + " HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("status line: %v", err)
	}
	var code int
	if _, err := fmt.Sscanf(strings.Fields(line)[1], "%d", &code); err != nil {
		t.Fatalf("parse status %q: %v", line, err)
	}
	return code
}

// ---- tests ----

func TestRoomName(t *testing.T) {
	a := relay.RoomName("secret", "device-a", "device-b")
	b := relay.RoomName("secret", "device-b", "device-a")
	if a != b {
		t.Fatalf("room name is not order-independent: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("room name length = %d, want 64", len(a))
	}
	for i := 0; i < len(a); i++ {
		c := a[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("room name %q is not lowercase hex", a)
		}
	}
	if relay.RoomName("other-secret", "device-a", "device-b") == a {
		t.Fatal("different secrets produced the same room name")
	}
}

// TestSpliceBidirectional is the core regression test: two clients join the
// same room and data flows both ways. It fails on the old server, which
// never flushed the 101 response and never marked a peer active, so no
// splice ever started.
func TestSpliceBidirectional(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	a, err := relay.Dial("ws://"+addr, "127.0.0.1:7420", "secret", "alice", "bob", 5*time.Second)
	if err != nil {
		t.Fatalf("dial a: %v", err)
	}
	defer a.Close()
	b, err := relay.Dial("ws://"+addr, "127.0.0.1:7420", "secret", "alice", "bob", 5*time.Second)
	if err != nil {
		t.Fatalf("dial b: %v", err)
	}
	defer b.Close()

	a.SetDeadline(time.Now().Add(5 * time.Second))
	b.SetDeadline(time.Now().Add(5 * time.Second))

	a.Write([]byte("hello"))
	if got := readN(t, b, 5); string(got) != "hello" {
		t.Fatalf("b got %q, want %q", got, "hello")
	}
	b.Write([]byte("world"))
	if got := readN(t, a, 5); string(got) != "world" {
		t.Fatalf("a got %q, want %q", got, "world")
	}
}

// TestLonePeerClosed: a room with one peer is closed after the wait.
func TestLonePeerClosed(t *testing.T) {
	addr := startServer(t, 300*time.Millisecond)
	a, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer a.Close()
	a.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := a.Read(buf); err == nil {
		t.Fatal("lone peer was not closed")
	}
}

// TestSpliceSurvivesRoomWait: a spliced session must outlive the room wait.
// The old server never stopped its timer, so every room died after the wait
// even with both peers present.
func TestSpliceSurvivesRoomWait(t *testing.T) {
	addr := startServer(t, 200*time.Millisecond)
	a, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatalf("dial a: %v", err)
	}
	defer a.Close()
	b, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatalf("dial b: %v", err)
	}
	defer b.Close()

	time.Sleep(500 * time.Millisecond) // well past the 200ms wait.

	a.SetDeadline(time.Now().Add(5 * time.Second))
	b.SetDeadline(time.Now().Add(5 * time.Second))
	a.Write([]byte("alive"))
	if got := readN(t, b, 5); string(got) != "alive" {
		t.Fatalf("b got %q, want %q (room died early)", got, "alive")
	}
}

// TestOversizedFrameClosesWith1009: a frame over MaxMessageBytes closes the
// room with 1009, and the other peer sees the same code.
func TestOversizedFrameClosesWith1009(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	room := relay.RoomName("s", "a", "b")
	ra := rawDial(t, addr, room)
	rb := rawDial(t, addr, room)
	// The relay closes the connection before the whole payload is written,
	// so the write may fail; the assertion is on what rb sees.
	_ = ra.sendFrame(0x2, make([]byte, relay.MaxMessageBytes+1), true)
	op, payload, err := rb.recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if op != 0x8 {
		t.Fatalf("opcode = %#x, want close (0x8)", op)
	}
	if code := closeCode(payload); code != 1009 {
		t.Fatalf("close code = %d, want 1009", code)
	}
}

// TestMaxFrameBoundary: a frame of exactly MaxMessageBytes passes through.
func TestMaxFrameBoundary(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	room := relay.RoomName("s", "a", "b")
	ra := rawDial(t, addr, room)
	rb := rawDial(t, addr, room)
	big := make([]byte, relay.MaxMessageBytes)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	ra.send(0x2, big, true)
	op, payload, err := rb.recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if op != 0x2 {
		t.Fatalf("opcode = %#x, want binary (0x2)", op)
	}
	if !bytes.Equal(payload, big) {
		t.Fatal("payload changed in transit")
	}
}

// TestTextFrameClosesWith1003: text frames carry no Synk traffic and close
// the room with 1003.
func TestTextFrameClosesWith1003(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	room := relay.RoomName("s", "a", "b")
	ra := rawDial(t, addr, room)
	rb := rawDial(t, addr, room)
	ra.send(0x1, []byte("text"), true)
	op, payload, err := rb.recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if op != 0x8 {
		t.Fatalf("opcode = %#x, want close (0x8)", op)
	}
	if code := closeCode(payload); code != 1003 {
		t.Fatalf("close code = %d, want 1003", code)
	}
}

// TestUnmaskedClientFrameRejected: RFC 6455 requires the server to close on
// an unmasked client frame.
func TestUnmaskedClientFrameRejected(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	room := relay.RoomName("s", "a", "b")
	ra := rawDial(t, addr, room)
	rb := rawDial(t, addr, room)
	ra.send(0x2, []byte("x"), false) // unmasked: protocol violation.
	op, payload, err := ra.recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if op != 0x8 {
		t.Fatalf("opcode = %#x, want close (0x8)", op)
	}
	if code := closeCode(payload); code != 1002 {
		t.Fatalf("close code = %d, want 1002", code)
	}
	// The other peer is torn down too, with a close frame.
	op, _, err = rb.recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if op != 0x8 {
		t.Fatalf("opcode = %#x, want close (0x8)", op)
	}
}

// TestClosePropagates: closing one end ends the session for the other.
func TestClosePropagates(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	a, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatalf("dial a: %v", err)
	}
	b, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatalf("dial b: %v", err)
	}
	defer b.Close()

	a.SetDeadline(time.Now().Add(5 * time.Second))
	b.SetReadDeadline(time.Now().Add(5 * time.Second))
	a.Close()
	start := time.Now()
	buf := make([]byte, 1)
	if _, err := b.Read(buf); err == nil {
		t.Fatal("b did not see the end of the session")
	}
	// The relay must propagate the close frame, so b sees the end of the
	// session promptly — not after its read deadline expires.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("b saw the end of the session only after %v; the close frame did not propagate", elapsed)
	}
}

// TestConcurrentRooms: two rooms splice independently.
func TestConcurrentRooms(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	a1, err := relay.Dial("ws://"+addr, "", "s1", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()
	b1, err := relay.Dial("ws://"+addr, "", "s1", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b1.Close()
	a2, err := relay.Dial("ws://"+addr, "", "s2", "c", "d", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	b2, err := relay.Dial("ws://"+addr, "", "s2", "c", "d", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()

	a1.SetDeadline(time.Now().Add(5 * time.Second))
	b1.SetDeadline(time.Now().Add(5 * time.Second))
	a2.SetDeadline(time.Now().Add(5 * time.Second))
	b2.SetDeadline(time.Now().Add(5 * time.Second))
	a1.Write([]byte("one"))
	a2.Write([]byte("two"))
	if got := readN(t, b1, 3); string(got) != "one" {
		t.Fatalf("b1 got %q, want %q", got, "one")
	}
	if got := readN(t, b2, 3); string(got) != "two" {
		t.Fatalf("b2 got %q, want %q", got, "two")
	}
}

// TestRoomFull: a third peer gets 409.
func TestRoomFull(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	room := relay.RoomName("s", "a", "b")
	a, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := relay.Dial("ws://"+addr, "", "s", "a", "b", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if code := upgradeStatus(t, addr, room); code != http.StatusConflict {
		t.Fatalf("third peer status = %d, want 409", code)
	}
}

// TestBadRoomPaths: malformed room names are 404.
func TestBadRoomPaths(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	paths := []string{
		"/v1/rooms/",
		"/v1/rooms/xyz",
		"/v1/rooms/" + strings.Repeat("a", 63),
		"/v1/rooms/" + strings.Repeat("a", 65),
		"/v1/rooms/" + strings.Repeat("A", 64), // uppercase is not allowed.
		"/v1/rooms/" + strings.Repeat("g", 64), // not hex.
	}
	for _, p := range paths {
		resp, err := http.Get("http://" + addr + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want 404", p, resp.StatusCode)
		}
	}
}

// TestNonWebSocketRejected: a plain GET to a valid room is not upgraded.
func TestNonWebSocketRejected(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	room := relay.RoomName("s", "a", "b")
	resp, err := http.Get("http://" + addr + "/v1/rooms/" + room)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}
}

// TestHealthEndpoint: /health answers 200.
func TestHealthEndpoint(t *testing.T) {
	addr := startServer(t, 2*time.Second)
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), `"ok":true`) {
		t.Fatalf("body = %q", body[:n])
	}
}
