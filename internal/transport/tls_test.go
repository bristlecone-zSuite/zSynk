package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"
)

type served struct {
	key     ed25519.PublicKey
	binding []byte
	err     error
}

func TestDevicesAuthenticateEachOther(t *testing.T) {
	server, serverKey := newCert(t)
	client, clientKey := newCert(t)
	addr, done := serveOnce(t, Config(server, func(ed25519.PublicKey) error { return nil }))

	c, err := Dial(addr, 5*time.Second, Config(client, pin(serverKey)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := PeerKey(c.ConnectionState())
	if err != nil || !got.Equal(serverKey) {
		t.Fatalf("client sees %x, %v", got, err)
	}
	binding, err := Binding(c.ConnectionState())
	if err != nil {
		t.Fatal(err)
	}
	s := <-done
	if s.err != nil || !s.key.Equal(clientKey) {
		t.Fatalf("server sees %x, %v", s.key, s.err)
	}
	if !bytes.Equal(s.binding, binding) || len(binding) != 32 {
		t.Fatalf("bindings differ: %x and %x", s.binding, binding)
	}
}

func TestHandshakeRefusals(t *testing.T) {
	server, serverKey := newCert(t)
	client, _ := newCert(t)
	_, otherKey := newCert(t)
	errNo := errors.New("not this key")
	acceptAll := Config(server, func(ed25519.PublicKey) error { return nil })
	for name, c := range map[string]struct {
		server, client *tls.Config
	}{
		"the client pins another key":         {acceptAll, Config(client, pin(otherKey))},
		"the server refuses the client's key": {Config(server, func(ed25519.PublicKey) error { return errNo }), Config(client, pin(serverKey))},
		"the client has no certificate": {acceptAll, func() *tls.Config {
			cfg := Config(client, pin(serverKey))
			cfg.Certificates = nil
			return cfg
		}()},
		"the client doesn't offer synk/2": {acceptAll, func() *tls.Config {
			cfg := Config(client, pin(serverKey))
			cfg.NextProtos = []string{"http/1.1"}
			return cfg
		}()},
		"the client offers no protocol": {acceptAll, func() *tls.Config {
			cfg := Config(client, pin(serverKey))
			cfg.NextProtos, cfg.VerifyConnection = nil, nil
			return cfg
		}()},
		"the client speaks TLS 1.2": {acceptAll, func() *tls.Config {
			cfg := Config(client, pin(serverKey))
			cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
			return cfg
		}()},
	} {
		addr, done := serveOnce(t, c.server)
		conn, err := Dial(addr, 5*time.Second, c.client)
		if err == nil {
			// In TLS 1.3 the client finishes first, so the server's
			// refusal arrives on the client's first read.
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = conn.Read(make([]byte, 1))
			_ = conn.Close()
		}
		s := <-done
		if err == nil || s.err == nil {
			t.Errorf("%s: client %v, server %v", name, err, s.err)
		}
	}
}

func serveOnce(t *testing.T, cfg *tls.Config) (string, <-chan served) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan served, 1)
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			done <- served{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		s := tls.Server(c, cfg)
		if err := s.Handshake(); err != nil {
			done <- served{err: err}
			return
		}
		key, err := PeerKey(s.ConnectionState())
		binding, _ := Binding(s.ConnectionState())
		done <- served{key, binding, err}
	}()
	return ln.Addr().String(), done
}

func newCert(t *testing.T) (tls.Certificate, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := Certificate(priv)
	if err != nil {
		t.Fatal(err)
	}
	return cert, pub
}

func pin(want ed25519.PublicKey) func(ed25519.PublicKey) error {
	return func(got ed25519.PublicKey) error {
		if !got.Equal(want) {
			return errors.New("not the pinned key")
		}
		return nil
	}
}
