package blob

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

// fakeSealer is a stand-in for the vault's cipher: it hides plaintext
// behind a prefix and a reversible scramble, and it fails loudly on
// anything that isn't its own envelope. Enough like the real thing for
// the store to prove its side of the contract.
type fakeSealer struct{}

const fakeMagic = "FAKESEAL1:"

func fakeScramble(b []byte) {
	for i := range b {
		b[i] ^= 0xA5
	}
}

func (fakeSealer) SealReader(r io.Reader, dest string) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	fakeScramble(data)
	out := append([]byte(fakeMagic), data...)
	return int64(len(out)), writeAtomic(dest, out)
}

func (fakeSealer) OpenSealed(src, out string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(raw, []byte(fakeMagic)) {
		return fmt.Errorf("not a sealed envelope")
	}
	payload := bytes.Clone(raw[len(fakeMagic):])
	fakeScramble(payload)
	return writeAtomic(out, payload)
}

func (fakeSealer) IsSealed(p string) bool {
	raw, err := os.ReadFile(p)
	return err == nil && bytes.HasPrefix(raw, []byte(fakeMagic))
}

func writeAtomic(dest string, data []byte) error {
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

func mustNewSealed(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir(), fakeSealer{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// needleScan fails if any needle appears in any file under root.
func needleScanBlob(t *testing.T, root string, needles ...string) {
	t.Helper()
	found := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, n := range needles {
			if bytes.Contains(raw, []byte(n)) {
				found[n] = p
			}
		}
		return nil
	})
	if len(found) > 0 {
		for n, p := range found {
			t.Errorf("plaintext %q readable at %s", n, p)
		}
	}
}

func TestSealedBlobsHidePlaintext(t *testing.T) {
	s := mustNewSealed(t)
	data := []byte("quarterly report contents")

	hash, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	if hash != atomicfile.HashBytes(data) {
		t.Fatalf("Put sealed hash: want plaintext hash, got %s", hash)
	}
	needleScanBlob(t, s.Root, "quarterly report contents")

	got, err := s.Get(hash)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get after sealing: got %q", got)
	}
	if !s.Has(hash) {
		t.Fatal("Has lost the sealed blob")
	}

	r, n, err := s.Open(hash)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n != int64(len(data)) {
		t.Fatalf("Open size: want %d, got %d", len(data), n)
	}
	all, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(all, data) {
		t.Fatalf("Open content: got %q", all)
	}
}

func TestSealedPutReaderAndPutFileHidePlaintext(t *testing.T) {
	s := mustNewSealed(t)
	data := []byte("meeting notes nobody may read from disk")

	hash, n, err := s.PutReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if hash != atomicfile.HashBytes(data) || n != int64(len(data)) {
		t.Fatalf("PutReader: hash %s len %d", hash, n)
	}
	got, err := s.Get(hash)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get after PutReader: %q (%v)", got, err)
	}

	src := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fhash, fn, err := s.PutFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if fhash != hash || fn != int64(len(data)) {
		t.Fatalf("PutFile: hash %s len %d", fhash, fn)
	}
	needleScanBlob(t, s.Root, "meeting notes nobody may read from disk")
}

func TestSealedBlobTamperIsCorrupt(t *testing.T) {
	s := mustNewSealed(t)
	data := []byte("tamper target")
	hash, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.pathFor(hash)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one payload byte, keeping the envelope shape.
	raw[len(raw)-1] ^= 0x01
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(hash); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get tampered sealed blob: want ErrCorrupt, got %v", err)
	}
	r, _, err := s.Open(hash)
	if err != nil {
		t.Fatalf("Open tampered sealed blob: %v", err)
	}
	_, readErr := io.ReadAll(r)
	_ = r.Close()
	if !errors.Is(readErr, ErrCorrupt) {
		t.Fatalf("reading tampered sealed blob: want ErrCorrupt, got %v", readErr)
	}
}

func TestPlainBlobsStillOpenWithSealer(t *testing.T) {
	root := t.TempDir()
	plain, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("stored before encryption was turned on")
	hash, err := plain.Put(data)
	if err != nil {
		t.Fatal(err)
	}

	sealed, err := New(root, fakeSealer{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealed.Get(hash)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("plain blob through sealing store: %q (%v)", got, err)
	}
}

func TestSealedBlobWithoutSealerIsCorrupt(t *testing.T) {
	root := t.TempDir()
	sealed, err := New(root, fakeSealer{})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("sealed content")
	hash, err := sealed.Put(data)
	if err != nil {
		t.Fatal(err)
	}

	bare, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Get(hash); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("sealed blob without a sealer: want ErrCorrupt, got %v", err)
	}
}

func TestSealAtRestRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := New(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("blob that starts plain and ends sealed")
	hash, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}

	s.SetSealer(fakeSealer{})
	if _, err := s.SealAtRest(); err != nil {
		t.Fatal(err)
	}
	needleScanBlob(t, root, "blob that starts plain and ends sealed")
	got, err := s.Get(hash)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get after SealAtRest: %q (%v)", got, err)
	}

	if _, err := s.UnsealAtRest(); err != nil {
		t.Fatal(err)
	}
	p, err := s.pathFor(hash)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "blob that starts plain and ends sealed") {
		t.Fatalf("UnsealAtRest left the blob sealed: %q", raw)
	}
	if got, err := s.Get(hash); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get after UnsealAtRest: %q (%v)", got, err)
	}
}
