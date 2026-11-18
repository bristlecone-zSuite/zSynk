// Package relay: client side.
//
// Dial connects to a relay and returns a net.Conn that carries the peer's
// TLS handshake and traffic through the relay's splice. The client performs
// the WebSocket upgrade itself (no net/http round-trip) so it can keep the
// raw connection and mask its frames as RFC 6455 requires.
package relay

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// RoomName derives the 64-character lowercase hex room name from the relay
// secret, the two device IDs, and a topic that separates a pair's rooms. A
// sync session's topic is the library ID, so two libraries syncing with the
// same peer through the relay at the same time land in different rooms
// instead of splicing into each other (decision 0013). Both devices compute
// the same name: the IDs are sorted lexicographically, so order does not
// matter.
//
// The secret derives from the paired peers' public keys (decision 0012,
// docs/PROTOCOL.md "Relay"): hex(SHA-256(min(kA,kB) || 0x00 || max(kA,kB)))
// over the raw 32-byte keys. A room name is a rendezvous label, not an
// authentication factor: knowing it lets an attacker occupy a slot, but the
// pinned mutual TLS handshake still decides who talks to whom.
func RoomName(secret string, myID, peerID, topic string) string {
	a, b := myID, peerID
	if a > b {
		a, b = b, a
	}
	h := sha256.New()
	io.WriteString(h, "synk/2 relay\x00")
	io.WriteString(h, a)
	io.WriteString(h, "\x00")
	io.WriteString(h, b)
	io.WriteString(h, "\x00")
	io.WriteString(h, secret)
	io.WriteString(h, "\x00")
	io.WriteString(h, topic)
	return hex.EncodeToString(h.Sum(nil))
}

// Dial connects to the relay at relayURL (ws:// or wss://), joins the room
// for (secret, myID, peerID, topic), and completes the WebSocket upgrade. It
// does not wait for the second peer; the Synk handshake over the returned
// conn has its own deadline.
//
// addr is the peer's direct address; it is used only for logging and
// diagnostics. The returned conn carries the raw (or TLS-encrypted)
// connection through which the Synk TLS handshake runs.
func Dial(relayURL, addr, secret string, myID, peerID, topic string, timeout time.Duration) (net.Conn, error) {
	room := RoomName(secret, myID, peerID, topic)
	u, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("relay: parse relay URL: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("relay: unsupported scheme %q (want ws or wss)", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("relay: missing host")
	}
	u.Scheme = "http"
	if strings.HasPrefix(relayURL, "wss") {
		u.Scheme = "https"
	}
	u.Path = "/v1/rooms/" + room
	u.RawQuery = ""
	u.Fragment = ""

	host := u.Host
	if u.Port() == "" {
		if strings.HasPrefix(relayURL, "wss") {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	d := net.Dialer{Timeout: timeout}
	raw, err := d.Dial("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("relay: dial %s: %w", host, err)
	}
	if strings.HasPrefix(relayURL, "wss") {
		raw = tls.Client(raw, &tls.Config{ServerName: u.Hostname()})
	}

	br := bufio.NewReader(raw)
	if err := wsUpgrade(br, raw, u, timeout); err != nil {
		raw.Close()
		return nil, fmt.Errorf("relay: upgrade %s: %w", u.Host, err)
	}

	c := &peerConn{conn: raw, r: br, w: raw, server: false}
	// The relay splices the room as soon as the second peer joins; until
	// then our frames sit in the relay's buffer. The Synk handshake has a
	// deadline of its own, so no extra wait is needed here.
	return c, nil
}

// wsUpgrade performs the client-side WebSocket upgrade on br/raw and
// validates the 101 response.
func wsUpgrade(br *bufio.Reader, raw net.Conn, u *url.URL, timeout time.Duration) error {
	_ = raw.SetDeadline(time.Now().Add(timeout))
	defer raw.SetDeadline(time.Time{})

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	req := "GET " + u.RequestURI() + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	if _, err := raw.Write([]byte(req)); err != nil {
		return err
	}

	resp, err := httpReadResponse(br)
	if err != nil {
		return err
	}
	if resp.status != 101 {
		return fmt.Errorf("unexpected status %d", resp.status)
	}
	accept := resp.header.Get("Sec-WebSocket-Accept")
	if accept != wsAccept(key) {
		return errors.New("bad Sec-WebSocket-Accept")
	}
	return nil
}

// httpReadResponse reads a minimal HTTP/1.1 response: status line plus
// headers. The body is not read (a 101 has none); any bytes beyond the
// headers stay in br for the frame reader.
func httpReadResponse(br *bufio.Reader) (wsResponse, error) {
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return wsResponse{}, err
	}
	parts := strings.SplitN(strings.TrimRight(statusLine, "\r\n"), " ", 3)
	if len(parts) < 2 {
		return wsResponse{}, errors.New("malformed status line")
	}
	var status int
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return wsResponse{}, errors.New("malformed status code")
		}
		status = status*10 + int(c-'0')
	}
	h := make(map[string][]string)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return wsResponse{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			return wsResponse{}, errors.New("malformed header")
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])
		h[k] = append(h[k], v)
	}
	return wsResponse{status: status, header: h}, nil
}

type wsHeader map[string][]string

func (h wsHeader) Get(key string) string {
	v := h[strings.ToLower(key)]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

type wsResponse struct {
	status int
	header wsHeader
}
