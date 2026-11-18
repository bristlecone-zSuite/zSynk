package vault

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func mustSalt(t *testing.T) []byte {
	t.Helper()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	return salt
}

func TestSealBytesRoundTrip(t *testing.T) {
	key := mustKey(t)
	plain := make([]byte, 64*1024)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	raw, err := sealBytes(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !isSealed(raw) {
		t.Fatal("sealed bytes should carry the envelope magic")
	}
	if isSealed(plain) {
		t.Fatal("plaintext must not look sealed")
	}
	back, err := openBytes(key, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatal("round trip changed the bytes")
	}
}

func TestOpenBytesRejectsWrongKeyAndTamper(t *testing.T) {
	key := mustKey(t)
	raw, err := sealBytes(key, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openBytes(mustKey(t), raw); err == nil {
		t.Fatal("a wrong key must not open the envelope")
	}
	tampered := append([]byte{}, raw...)
	tampered[len(tampered)-1] ^= 1
	if _, err := openBytes(key, tampered); err == nil {
		t.Fatal("a flipped bit must not authenticate")
	}
	if _, err := openBytes(key, raw[:len(raw)-3]); err == nil {
		t.Fatal("a truncated envelope must not authenticate")
	}
	if _, err := openBytes(key, []byte("not sealed at all")); err == nil {
		t.Fatal("bytes without the magic must be rejected")
	}
}

func TestSealFileStreamsLargeContent(t *testing.T) {
	key := mustKey(t)
	// Two full chunks and a partial one: chunk boundaries and a final
	// partial chunk must both survive.
	plain := make([]byte, 2*chunkBytes+12345)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	want := sha256Of(plain)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "plain")
	if err := os.WriteFile(src, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(tmp, "sealed")
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	size, err := sealFile(key, f, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(plain)) {
		t.Fatalf("size = %d, want %d", size, len(plain))
	}
	// The sealed file must not carry a readable prefix of the plaintext.
	raw, err := os.ReadFile(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if isSealed(raw) == false {
		t.Fatal("sealed file lacks the envelope magic")
	}
	if bytes.Contains(raw, plain[:4096]) {
		t.Fatal("plaintext leaked into the sealed file")
	}
	chunks := (len(plain) + chunkBytes - 1) / chunkBytes
	wantSealed := int64(len(plain)) + int64(overheadBytes) + int64(chunks-1)*16
	if int64(len(raw)) != wantSealed {
		t.Fatalf("sealed size = %d, want plaintext + %d + %d tags", len(raw), overheadBytes, chunks)
	}
	got := filepath.Join(tmp, "back")
	if err := openFileTo(key, sealed, got); err != nil {
		t.Fatal(err)
	}
	if sha256File(t, got) != want {
		t.Fatal("decrypted content differs")
	}
}

func TestSealFileEmptyContent(t *testing.T) {
	key := mustKey(t)
	tmp := t.TempDir()
	sealed := filepath.Join(tmp, "sealed")
	size, err := sealFile(key, strings.NewReader(""), sealed)
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Fatalf("size = %d, want 0", size)
	}
	got := filepath.Join(tmp, "back")
	if err := openFileTo(key, sealed, got); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("decrypted empty content is %d bytes", info.Size())
	}
}

func TestOpenFileToRejectsPlainFile(t *testing.T) {
	key := mustKey(t)
	tmp := t.TempDir()
	plain := filepath.Join(tmp, "plain")
	if err := os.WriteFile(plain, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := openFileTo(key, plain, filepath.Join(tmp, "out")); err == nil {
		t.Fatal("decrypting a plain file must fail, not yield ciphertext")
	}
}

func TestDeriveKeyDeterministicAndSalted(t *testing.T) {
	salt := mustSalt(t)
	a := deriveKey("correct horse battery staple", salt)
	b := deriveKey("correct horse battery staple", salt)
	if !bytes.Equal(a, b) {
		t.Fatal("same passphrase and salt derived different keys")
	}
	if len(a) != keyBytes {
		t.Fatalf("key is %d bytes, want %d", len(a), keyBytes)
	}
	if bytes.Equal(a, deriveKey("correct horse battery staple", mustSalt(t))) {
		t.Fatal("a different salt must derive a different key")
	}
	if bytes.Equal(a, deriveKey("wrong horse", salt)) {
		t.Fatal("a different passphrase must derive a different key")
	}
}

func TestKeycheckRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := writeKeycheck(root, "passphrase one"); err != nil {
		t.Fatal(err)
	}
	key, err := keycheckKey(root, "passphrase one")
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != keyBytes {
		t.Fatalf("key is %d bytes", len(key))
	}
	if _, err := keycheckKey(root, "passphrase two"); err == nil {
		t.Fatal("the wrong passphrase must not open the keycheck")
	}
	// A second keycheck replaces the first; the new passphrase takes over.
	if err := writeKeycheck(root, "passphrase two"); err != nil {
		t.Fatal(err)
	}
	if _, err := keycheckKey(root, "passphrase one"); err == nil {
		t.Fatal("the replaced passphrase must not open the new keycheck")
	}
}

func sha256Of(b []byte) string {
	sum := sha256.Sum256(b)
	return hexOf(sum)
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	sum := h.Sum(nil)
	return hexOf([sha256.Size]byte(sum))
}
