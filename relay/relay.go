// Package relay implements a self-hostable WebSocket relay for Synk.
//
// The relay splices two WebSockets that join the same room. Peers run their
// mutually authenticated TLS 1.3 session end-to-end through the splice, so
// the relay only ever forwards ciphertext. Room names are 64 lowercase hex
// characters (256 bits) derived from a secret both devices share after
// pairing. A room name is a rendezvous secret, not an authentication factor:
// whoever learns it can occupy a slot, but cannot complete the pinned TLS
// handshake.
//
// Server usage:
//
//	srv := relay.NewServer()
//	srv.ListenAndServe(":7430")
//
// Accepts WebSocket upgrades at /v1/rooms/<64-hex-chars>. Binary frames only,
// each at most 1 MiB + 64 KiB. Text frames close the room with code 1003,
// oversized messages close it with 1009.
package relay

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is required by RFC 6455 for the accept key.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxMessageBytes is the largest binary frame the relay forwards, matching
// the hosted worker: Synk's own frames are at most 1 MiB plus framing and
// TLS record overhead.
const MaxMessageBytes = (1 << 20) + 64*1024

// errOversized and errProtocol mark protocol-level failures so the relay
// can close the room with the right WebSocket close code (1009 and 1002).
var (
	errOversized = errors.New("relay: message too large")
	errProtocol  = errors.New("relay: protocol violation")
)

// RoomWait is how long a room holds a lone first peer before closing it.
// A splice with one end missing is useless.
const RoomWait = 30 * time.Second

// Server is a relay server. It accepts WebSocket connections at
// /v1/rooms/<room> where room is 64 lowercase hex characters, splices the
// two peers of each room, and closes a room when either peer leaves.
type Server struct {
	mu    sync.Mutex
	rooms map[string]*room

	// Wait is how long a room holds a lone first peer before closing it.
	// Zero or negative uses the default (RoomWait).
	Wait time.Duration
}

// NewServer returns a ready relay server.
func NewServer() *Server {
	return &Server{rooms: map[string]*room{}}
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"name":"synk-relay"}`)
	})
	mux.HandleFunc("/", s.serve)
	return mux
}

// ListenAndServe serves plain HTTP on addr until the listener fails.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("relay: listening on %s", addr)
	return http.Serve(ln, s.Handler())
}

// ListenAndServeTLS serves HTTPS on addr with the given certificate.
func (s *Server) ListenAndServeTLS(addr, certFile, keyFile string) error {
	log.Printf("relay: listening on %s (TLS)", addr)
	return http.ListenAndServeTLS(addr, certFile, keyFile, s.Handler())
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"name":"synk-relay"}`)
		return
	}
	path := r.URL.Path
	if !strings.HasPrefix(path, "/v1/rooms/") {
		http.NotFound(w, r)
		return
	}
	roomName := strings.TrimPrefix(path, "/v1/rooms/")
	if len(roomName) != 64 || !isHex(roomName) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected websocket upgrade", http.StatusUpgradeRequired)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server cannot splice", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	rm := s.rooms[roomName]
	if rm == nil {
		rm = newRoom(s, roomName)
		s.rooms[roomName] = rm
	}
	slot := rm.claim()
	s.mu.Unlock()
	if slot < 0 {
		http.Error(w, "room full", http.StatusConflict)
		return
	}

	raw, bufrw, err := hj.Hijack()
	if err != nil {
		rm.release(slot)
		log.Printf("relay: room %.8s…: hijack: %v", roomName, err)
		return
	}
	if err := wsHandshake(bufrw, r); err != nil {
		raw.Close()
		rm.release(slot)
		log.Printf("relay: room %.8s…: upgrade: %v", roomName, err)
		return
	}

	c := &peerConn{conn: raw, r: bufrw.Reader, w: bufrw.Writer, server: true}
	rm.servePeer(slot, c)
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// room holds one slot per peer. At most two peers may hold a slot.
type room struct {
	srv  *Server
	name string

	mu      sync.Mutex
	slots   [2]*peerConn
	ready   [2]chan struct{} // closed when the slot holds a live peer
	spliced chan struct{}    // closed when the splice has started
	closed  bool
	dead    chan struct{} // closed when the room tears down
	timer   *time.Timer
}

func newRoom(srv *Server, name string) *room {
	rm := &room{
		srv:     srv,
		name:    name,
		dead:    make(chan struct{}),
		spliced: make(chan struct{}),
		ready:   [2]chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	wait := srv.Wait
	if wait <= 0 {
		wait = RoomWait
	}
	rm.timer = time.AfterFunc(wait, func() {
		rm.mu.Lock()
		live := rm.readyClosedLocked(0) && rm.readyClosedLocked(1)
		if live {
			rm.mu.Unlock()
			return // both peers are here; the session is running
		}
		// Close under the lock so a peer that becomes ready at the same
		// time sees rm.closed and gets 1001 instead of a half-dead room.
		rm.closeLocked(1001, "no peer joined")
	})
	return rm
}

// claim reserves an empty slot, -1 when the room is full or closed.
func (rm *room) claim() int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.closed {
		return -1
	}
	for i := range rm.slots {
		if rm.slots[i] == nil {
			rm.slots[i] = &peerConn{} // placeholder until the upgrade succeeds
			return i
		}
	}
	return -1
}

// release gives back a claimed slot (the upgrade failed).
func (rm *room) release(slot int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.slots[slot] != nil && rm.slots[slot].conn == nil {
		rm.slots[slot] = nil
	}
}

// servePeer runs one peer's end of the splice. It returns when the room
// tears down.
func (rm *room) servePeer(slot int, c *peerConn) {
	rm.mu.Lock()
	if rm.closed {
		rm.mu.Unlock()
		c.closePeer(1001, "room closed")
		return
	}
	rm.slots[slot] = c
	close(rm.ready[slot])
	rm.mu.Unlock()

	// Wait for the other peer to be live, or the room to die.
	select {
	case <-rm.ready[1-slot]:
	case <-rm.dead:
		return
	}

	// Both peers are live. Exactly one of them runs the splice (which
	// forwards both directions); the other waits for the room to die.
	rm.mu.Lock()
	if rm.timer != nil {
		rm.timer.Stop()
		rm.timer = nil
	}
	other := rm.slots[1-slot]
	splicing := false
	if other != nil {
		select {
		case <-rm.spliced:
		default:
			close(rm.spliced)
			splicing = true
		}
	}
	rm.mu.Unlock()
	if !splicing {
		<-rm.dead
		return
	}
	splice(rm, c, other)
}

// readyClosedLocked reports whether the given slot already holds a live
// peer. Callers hold rm.mu.
func (rm *room) readyClosedLocked(slot int) bool {
	select {
	case <-rm.ready[slot]:
		return true
	default:
		return false
	}
}

// splice forwards frames both ways until either end fails, then tears the
// room down.
func splice(rm *room, a, b *peerConn) {
	// A buffer of MaxMessageBytes+1 keeps one message in one frame:
	// peerConn.Read returns at most one frame's payload per call, and
	// peerConn.Write wraps each Write in a frame. Each direction needs its
	// own buffer; they run concurrently.
	bufAB := make([]byte, MaxMessageBytes+1)
	bufBA := make([]byte, MaxMessageBytes+1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.CopyBuffer(b, a, bufAB)
		if err != nil {
			log.Printf("relay: room %.8s…: forward: %v", rm.name, err)
		}
		// Tear down on EOF as well as error: a clean close on one end
		// must end the session for the other, not leave it hanging.
		rm.teardown(a, b)
	}()
	_, err := io.CopyBuffer(a, b, bufBA)
	if err != nil {
		log.Printf("relay: room %.8s…: forward: %v", rm.name, err)
	}
	// Tear down now so the other direction's blocked read fails and the
	// goroutine above can exit.
	rm.teardown(a, b)
	<-done
}

// teardown closes the room with the code the failing end was closed with,
// so the other peer sees the same reason (1009 oversized, 1003 text, 1002
// protocol, 1000 normal). It is idempotent.
func (rm *room) teardown(a, b *peerConn) {
	code, reason := a.closeInfo()
	if code == 0 {
		code, reason = b.closeInfo()
	}
	if code == 0 {
		code, reason = 1000, "session complete"
	}
	rm.close(code, reason)
}

// closeLocked tears the room down: both ends get a close frame and the
// room leaves the server's table. Callers hold rm.mu. It is safe to call
// more than once.
func (rm *room) closeLocked(code int, reason string) {
	if rm.closed {
		return
	}
	rm.closed = true
	slots := rm.slots
	rm.slots = [2]*peerConn{}
	if rm.timer != nil {
		rm.timer.Stop()
		rm.timer = nil
	}
	rm.mu.Unlock()
	close(rm.dead)

	for _, c := range slots {
		if c != nil && c.conn != nil {
			c.closePeer(code, reason)
		}
	}
	rm.srv.mu.Lock()
	if cur, ok := rm.srv.rooms[rm.name]; ok && cur == rm {
		delete(rm.srv.rooms, rm.name)
	}
	rm.srv.mu.Unlock()
	log.Printf("relay: room %.8s…: closed (%d %s)", rm.name, code, reason)
}

// close tears the room down; see closeLocked.
func (rm *room) close(code int, reason string) {
	rm.mu.Lock()
	rm.closeLocked(code, reason)
}

// wsHandshake writes the 101 response for r's handshake and flushes it.
func wsHandshake(w *bufio.ReadWriter, r *http.Request) error {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return errors.New("missing Sec-WebSocket-Key")
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
	if _, err := w.WriteString(resp); err != nil {
		return err
	}
	return w.Flush()
}

func wsAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte("258EAFA5-E914-47DA-95CA-5AB5DC106C7B"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// ---- WebSocket framing (RFC 6455, the subset Synk needs) ----

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

// frame is one decoded WebSocket frame.
type frame struct {
	opcode  int
	payload []byte
}

// readFrame reads one frame from r. mustMask says whether the peer is
// required to mask its payload (clients must; servers must not).
func readFrame(r io.Reader, mustMask bool) (frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frame{}, err
	}
	f := frame{opcode: int(head[0] & 0x0f)}
	if head[0]&0x70 != 0 {
		return frame{}, errors.New("relay: reserved frame bits set")
	}
	if head[0]&0x80 == 0 {
		return frame{}, errors.New("relay: fragmented frames not supported")
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > MaxMessageBytes {
		return frame{}, fmt.Errorf("%w: frame of %d bytes, more than the %d allowed", errOversized, n, MaxMessageBytes)
	}
	masked := head[1]&0x80 != 0
	if mustMask && !masked {
		return frame{}, fmt.Errorf("%w: client frame is not masked", errProtocol)
	}
	if !mustMask && masked {
		return frame{}, fmt.Errorf("%w: server frame is masked", errProtocol)
	}
	var payload []byte
	if masked {
		var key [4]byte
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return frame{}, err
		}
		payload = make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return frame{}, err
		}
		for i := range payload {
			payload[i] ^= key[i&3]
		}
	} else {
		payload = make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return frame{}, err
		}
	}
	f.payload = payload
	return f, nil
}

// writeFrame writes one frame to w. sendMask: clients must mask their
// frames; servers must not.
func writeFrame(w io.Writer, opcode int, payload []byte, sendMask bool) error {
	if len(payload) > MaxMessageBytes {
		return fmt.Errorf("relay: frame of %d bytes, more than the %d allowed", len(payload), MaxMessageBytes)
	}
	var head [14]byte
	head[0] = byte(0x80 | opcode) // FIN set: one unfragmented frame.
	n := len(payload)
	pos := 2
	switch {
	case n <= 125:
		head[1] = byte(n)
	case n <= 65535:
		head[1] = 126
		binary.BigEndian.PutUint16(head[2:4], uint16(n))
		pos = 4
	default:
		head[1] = 127
		binary.BigEndian.PutUint64(head[2:10], uint64(n))
		pos = 10
	}
	if sendMask {
		head[1] |= 0x80
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		copy(head[pos:], key[:])
		pos += 4
		masked := make([]byte, len(payload))
		for i := range payload {
			masked[i] = payload[i] ^ key[i&3]
		}
		// A partial Write would truncate the frame and corrupt the
		// ciphertext stream.
		if err := writeFull(w, head[:pos]); err != nil {
			return err
		}
		return writeFull(w, masked)
	}
	if err := writeFull(w, head[:pos]); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return writeFull(w, payload)
}

// writeFull writes all of buf to w. A partial Write would truncate a frame
// and corrupt the ciphertext stream, so keep writing until the buffer is
// gone or the connection fails.
func writeFull(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// closePayload encodes a close frame's body.
func closePayload(code int, reason string) []byte {
	p := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(p, uint16(code))
	copy(p[2:], reason)
	return p
}

// peerConn is one connected peer. On the server side it reads the client's
// masked frames and writes unmasked ones; on the client side the reverse.
type peerConn struct {
	conn   net.Conn
	r      io.Reader
	w      io.Writer
	server bool

	mu          sync.Mutex
	closed      bool
	closeCode   int // set when closed with a close frame; 0 otherwise
	closeReason string
	pending     []byte // unread remainder of the current frame
}

// Read returns the next binary frame's payload, in chunks of at most len(p).
// Control frames are answered or swallowed; text frames and protocol
// violations close the connection and return an error.
func (c *peerConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.pending) == 0 {
		f, err := readFrame(c.r, c.server)
		if err != nil {
			switch {
			case errors.Is(err, errOversized):
				c.closePeer(1009, "message too large")
			case errors.Is(err, errProtocol):
				c.closePeer(1002, "protocol error")
			default:
				c.shutdown()
			}
			return 0, err
		}
		switch f.opcode {
		case opBinary:
			c.pending = f.payload
		case opPing:
			_ = c.writeControl(opPong, f.payload)
		case opPong, opContinuation:
			// Nothing to do.
		case opClose:
			// RFC 6455 §5.5.1: answer a close frame with a close frame,
			// echoing the code and reason, and record them so the other
			// peer of the splice sees the same.
			code := 1000
			reason := ""
			if len(f.payload) >= 2 {
				code = int(binary.BigEndian.Uint16(f.payload))
				reason = string(f.payload[2:])
			}
			c.closePeer(code, reason)
			return 0, io.EOF
		case opText:
			c.closePeer(1003, "text frames carry no Synk traffic")
			return 0, errors.New("relay: text frame")
		default:
			c.closePeer(1003, "unexpected opcode")
			return 0, fmt.Errorf("relay: opcode %d", f.opcode)
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// Write sends p as one binary frame.
func (c *peerConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, errors.New("relay: connection closed")
	}
	if err := writeFrame(c.w, opBinary, p, !c.server); err != nil {
		return 0, err
	}
	if bw, ok := c.w.(*bufio.Writer); ok {
		if err := bw.Flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (c *peerConn) writeControl(opcode int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("relay: connection closed")
	}
	if err := writeFrame(c.w, opcode, payload, !c.server); err != nil {
		return err
	}
	if bw, ok := c.w.(*bufio.Writer); ok {
		return bw.Flush()
	}
	return nil
}

// closePeer sends a close frame and closes the connection.
func (c *peerConn) closePeer(code int, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeCode = code
	c.closeReason = reason
	_ = writeFrame(c.w, opClose, closePayload(code, reason), !c.server)
	if bw, ok := c.w.(*bufio.Writer); ok {
		_ = bw.Flush()
	}
	c.mu.Unlock()
	_ = c.conn.Close()
}

// shutdown closes without a close frame (after an error or EOF).
func (c *peerConn) shutdown() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.conn.Close()
}

// closeInfo reports the close code and reason if this peer was closed with
// a close frame, or (0, "") if it was not.
func (c *peerConn) closeInfo() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCode, c.closeReason
}

// LocalAddr and the rest implement net.Conn.
func (c *peerConn) Close() error {
	c.closePeer(1000, "")
	return nil
}
func (c *peerConn) LocalAddr() net.Addr           { return c.conn.LocalAddr() }
func (c *peerConn) RemoteAddr() net.Addr          { return c.conn.RemoteAddr() }
func (c *peerConn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }
func (c *peerConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}
func (c *peerConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
