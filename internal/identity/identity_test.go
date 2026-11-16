package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	d, priv, err := LoadOrCreate(dir, "Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	if d.DeviceID == "" || priv == nil {
		t.Fatal("empty identity")
	}
	d2, priv2, err := LoadOrCreate(dir, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if d2.DeviceID != d.DeviceID {
		t.Fatal("id changed")
	}
	if !priv.Equal(priv2) {
		t.Fatal("key changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatal(err)
	}
}

func TestIDStable(t *testing.T) {
	a, _, err := Generate("a")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := a.Public()
	if err != nil {
		t.Fatal(err)
	}
	if IDFromPublic(pub) != a.DeviceID {
		t.Fatal("id mismatch")
	}
}

func TestTamperDetected(t *testing.T) {
	dir := t.TempDir()
	d, _, err := LoadOrCreate(dir, "A")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "identity.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = d
	tampered := []byte(`{"version":1,"name":"x","deviceId":"NOPE","publicKey":"AA","privateKey":"BB"}`)
	if err := os.WriteFile(p, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dir, "A"); err == nil {
		t.Fatal("expected failure on tamper")
	}
	_ = raw
}

func TestFingerprint(t *testing.T) {
	d, _, err := Generate("Mac")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublic(d.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(must(d.Public())) || EncodePublic(pub) != d.PublicKey {
		t.Fatal("EncodePublic and ParsePublic don't round-trip")
	}
	fp := Fingerprint(pub)
	if len(fp) != 64 || strings.ToLower(fp) != fp {
		t.Fatalf("fingerprint %q", fp)
	}
	other, _, err := Generate("Phone")
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(must(other.Public())) == fp {
		t.Fatal("two keys share a fingerprint")
	}
	pasted := strings.ToUpper(fp[:32]) + " : " + fp[32:48] + "-" + fp[48:]
	if got, err := ParseFingerprint(pasted); err != nil || got != fp {
		t.Fatalf("ParseFingerprint(%q) = %q, %v", pasted, got, err)
	}
	for _, bad := range []string{"", fp[:62], fp + "00", "zz" + fp[2:]} {
		if _, err := ParseFingerprint(bad); err == nil {
			t.Errorf("ParseFingerprint(%q) accepted it", bad)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
