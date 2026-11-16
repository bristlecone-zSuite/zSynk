package pairing

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const tokenBytes = 16

// Session is a pairing offer's secrets: a short code a person can read
// and a token with the offer's real strength.
type Session struct {
	Code  string
	Token string
}

func NewSession() (*Session, error) {
	tok := make([]byte, tokenBytes)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	codeBytes := make([]byte, 8)
	if _, err := rand.Read(codeBytes); err != nil {
		return nil, err
	}
	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteByte(alphabet[int(codeBytes[i])%len(alphabet)])
	}
	code := b.String()
	return &Session{Code: code[:4] + "-" + code[4:], Token: hex.EncodeToString(tok)}, nil
}

func Normalize(code string) string {
	s := strings.ToUpper(strings.TrimSpace(code))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// The two directions of a pairing proof. Each end checks the other's role,
// so a proof sent back to the device that made it doesn't verify.
const (
	RoleJoin   = "join"
	RoleAccept = "accept"
)

// ErrBadToken means a token isn't in the form NewSession makes.
var ErrBadToken = fmt.Errorf("pairing: a token is %d hex digits", 2*tokenBytes)

// Proof shows that one end of a connection knows an offer's code and token
// without revealing them. binding is the connection's TLS binding, so a
// proof can't be replayed on, or relayed to, another connection.
func Proof(code, token, role string, binding []byte) (string, error) {
	key, err := hex.DecodeString(strings.TrimSpace(token))
	if err != nil || len(key) != tokenBytes {
		return "", ErrBadToken
	}
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "synk/2 pairing %s\x00%s\x00", role, Normalize(code))
	m.Write(binding)
	return hex.EncodeToString(m.Sum(nil)), nil
}

// CheckProof reports whether proof is Proof(code, token, role, binding).
func CheckProof(code, token, role string, binding []byte, proof string) bool {
	want, err := Proof(code, token, role, binding)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(strings.ToLower(proof)))
}

// Link is what a synk://pair link, usually shown as a QR code, carries.
type Link struct {
	Code  string
	Token string
	// Fingerprint is the offering device's key fingerprint; the joining
	// device accepts no other key.
	Fingerprint string
	// Addrs are where the offering device listens.
	Addrs []string
}

// URI encodes l as a synk://pair link.
func (l Link) URI() string {
	q := url.Values{}
	q.Set("v", "2")
	q.Set("code", l.Code)
	q.Set("token", l.Token)
	q.Set("fp", l.Fingerprint)
	for _, a := range l.Addrs {
		q.Add("addr", a)
	}
	return "synk://pair?" + q.Encode()
}

// ParseURI reads a link that Link.URI made.
func ParseURI(s string) (Link, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != "synk" || u.Host != "pair" {
		return Link{}, errors.New("pairing: not a synk://pair link")
	}
	q := u.Query()
	if v := q.Get("v"); v != "2" {
		return Link{}, fmt.Errorf("pairing: this Synk can't read version %q pairing links", v)
	}
	l := Link{Code: q.Get("code"), Token: q.Get("token"), Fingerprint: q.Get("fp"), Addrs: q["addr"]}
	if l.Code == "" || l.Token == "" || l.Fingerprint == "" {
		return Link{}, errors.New("pairing: the link has no code, token, or fingerprint")
	}
	return l, nil
}
