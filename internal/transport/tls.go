package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// ALPN names this version of the peer protocol. A later version offers its
// own name first and this one after it while it still speaks this one.
const ALPN = "synk/2"

// MaxPreAuthFrame bounds the frames read before the other device is known
// to be paired, so a stranger can't make this one allocate much.
const MaxPreAuthFrame = 64 << 10

// bindingLabel is the exporter label for Binding.
const bindingLabel = "EXPORTER-synk-binding"

// Certificate returns a self-signed certificate for key. Devices pin each
// other's keys, so nothing checks the certificate's names or dates.
func Certificate(key ed25519.PrivateKey) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "synk"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Config returns the TLS configuration for either end of a connection
// between devices. Both ends present cert. check sees the key the other end
// proved it holds and fails the handshake by returning an error.
func Config(cert tls.Certificate, check func(ed25519.PublicKey) error) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
		ClientAuth:   tls.RequireAnyClientCert,
		// Devices are authenticated by the key check in VerifyConnection,
		// not by a certificate authority.
		InsecureSkipVerify: true,
		// Every connection proves both keys afresh; sessions are long
		// enough that resumption would save nothing.
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.NegotiatedProtocol != ALPN {
				return fmt.Errorf("transport: the other device doesn't speak %s", ALPN)
			}
			pub, err := PeerKey(cs)
			if err != nil {
				return err
			}
			return check(pub)
		},
	}
}

// PeerKey returns the key the other end of a completed handshake holds.
func PeerKey(cs tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(cs.PeerCertificates) != 1 {
		return nil, errors.New("transport: the other device must present exactly one certificate")
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("transport: the other device's key isn't Ed25519")
	}
	return pub, nil
}

// Binding returns a value that both ends of one connection compute and no
// other connection shares, so a proof that includes it can't be replayed
// or relayed onto another connection.
func Binding(cs tls.ConnectionState) ([]byte, error) {
	return cs.ExportKeyingMaterial(bindingLabel, nil, 32)
}

// Dial connects to addr and completes the handshake within timeout.
func Dial(addr string, timeout time.Duration, cfg *tls.Config) (*tls.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return c.(*tls.Conn), nil
}

// DialConn completes the handshake on a connection that is already open —
// the way a relay splice hands a connection over — with the same pinned
// keys as Dial. On error the connection is closed.
func DialConn(raw net.Conn, timeout time.Duration, cfg *tls.Config) (*tls.Conn, error) {
	c := tls.Client(raw, cfg)
	_ = raw.SetDeadline(time.Now().Add(timeout))
	if err := c.Handshake(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return c, nil
}
