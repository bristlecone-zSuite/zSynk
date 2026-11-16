package pairing

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCodeNormalize(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if Normalize(s.Code) != Normalize(strings.ToLower(s.Code)) || Normalize(" ab cd-ef ") != "ABCDEF" {
		t.Fatal("codes differ by case or separators")
	}
}

func TestProof(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	binding := []byte("connection one")
	proof, err := Proof(s.Code, s.Token, RoleJoin, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !CheckProof(strings.ToLower(s.Code), s.Token, RoleJoin, binding, strings.ToUpper(proof)) {
		t.Fatal("a proof doesn't verify")
	}
	for name, ok := range map[string]bool{
		"another role":       CheckProof(s.Code, s.Token, RoleAccept, binding, proof),
		"another connection": CheckProof(s.Code, s.Token, RoleJoin, []byte("connection two"), proof),
		"another code":       CheckProof(other.Code, s.Token, RoleJoin, binding, proof),
		"another token":      CheckProof(s.Code, other.Token, RoleJoin, binding, proof),
		"no proof":           CheckProof(s.Code, s.Token, RoleJoin, binding, ""),
	} {
		if ok {
			t.Errorf("a proof verified with %s", name)
		}
	}
	for _, bad := range []string{"", "00", s.Token + "00", "zz" + s.Token[2:]} {
		if _, err := Proof(s.Code, bad, RoleJoin, binding); !errors.Is(err, ErrBadToken) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
}

func TestLinkRoundTrip(t *testing.T) {
	l := Link{Code: "AB12-CD34", Token: strings.Repeat("0f", 16), Fingerprint: strings.Repeat("ab", 32), Addrs: []string{"192.168.1.5:7420", "[fd00::5]:7420"}}
	uri := l.URI()
	if !strings.HasPrefix(uri, "synk://pair?") {
		t.Fatalf("link %q", uri)
	}
	got, err := ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != l.Code || got.Token != l.Token || got.Fingerprint != l.Fingerprint || !slices.Equal(got.Addrs, l.Addrs) {
		t.Fatalf("%+v became %+v", l, got)
	}
	for _, bad := range []string{
		"https://pair?v=2&code=A&token=B&fp=C",
		"synk://other?v=2&code=A&token=B&fp=C",
		"synk://pair?code=A&token=B&fp=C",
		"synk://pair?v=3&code=A&token=B&fp=C",
		"synk://pair?v=2&code=A&token=B",
	} {
		if _, err := ParseURI(bad); err == nil {
			t.Errorf("ParseURI(%q) accepted it", bad)
		}
	}
}

func TestPendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := SavePending(dir, &Pending{Code: s.Code, Token: s.Token}); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPending(dir)
	if err != nil || p == nil {
		t.Fatalf("%v %#v", err, p)
	}
	if p.Code != s.Code || p.Token != s.Token {
		t.Fatal(p)
	}
	if err := SavePending(dir, &Pending{Code: s.Code, Token: s.Token, ExpiresAt: time.Now().UTC().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	p, err = LoadPending(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Fatal("expired offer must vanish")
	}
}
