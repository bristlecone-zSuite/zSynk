package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	gohash "hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

var ErrNotFound = errors.New("blob: not found")
var ErrCorrupt = errors.New("blob: content does not match hash")
var ErrInvalidHash = errors.New("blob: hash must be 64 lowercase hex characters")

// Store is a content-addressed object store. Objects are immutable.
type Store struct {
	Root string
}

// New opens the store at root. Content still on its way into the store
// when a process last stopped is discarded, so only one Store may use a
// root at a time. What interrupted transfers staged under partial/ is
// kept, for the next transfer to continue.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "objects"), 0o700); err != nil {
		return nil, err
	}
	s := &Store{Root: root}
	if err := os.RemoveAll(s.tmpDir()); err != nil {
		return nil, err
	}
	if err := os.Mkdir(s.tmpDir(), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.partialDir(), 0o700); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) tmpDir() string { return filepath.Join(s.Root, "tmp") }

func (s *Store) pathFor(hash string) (string, error) {
	if err := ValidateHash(hash); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, "objects", hash[:2], hash[2:4], hash), nil
}

// ValidateHash accepts only the exact form HashBytes produces. Hashes arrive
// from peers and become file names, so nothing is trimmed or case-folded.
func ValidateHash(hash string) error {
	if len(hash) != 64 {
		return ErrInvalidHash
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return ErrInvalidHash
		}
	}
	return nil
}

func (s *Store) Has(hash string) bool {
	p, err := s.pathFor(hash)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func (s *Store) Put(data []byte) (string, error) {
	hash := atomicfile.HashBytes(data)
	p, err := s.pathFor(hash)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return hash, nil
	}
	if err := atomicfile.WriteFile(p, data, 0o600); err != nil {
		return "", err
	}
	return hash, nil
}

func (s *Store) Get(hash string) ([]byte, error) {
	p, err := s.pathFor(hash)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	got := atomicfile.HashBytes(b)
	if got != hash {
		return nil, fmt.Errorf("%w: expected %s got %s", ErrCorrupt, hash, got)
	}
	return b, nil
}

// Open streams a blob. The reader fails with ErrCorrupt at EOF if the
// content on disk doesn't match the hash.
func (s *Store) Open(hash string) (io.ReadCloser, int64, error) {
	p, err := s.pathFor(hash)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return verifiedFile{Reader: Verify(f, hash, st.Size()), f: f}, st.Size(), nil
}

type verifiedFile struct {
	io.Reader
	f *os.File
}

func (v verifiedFile) Close() error { return v.f.Close() }

// Verify passes reads through from r. At EOF it returns ErrCorrupt instead
// unless exactly size bytes hashing to hash were read.
func Verify(r io.Reader, hash string, size int64) io.Reader {
	return &verifier{r: r, h: sha256.New(), want: hash, size: size}
}

type verifier struct {
	r    io.Reader
	h    gohash.Hash
	want string
	size int64
	n    int64
}

func (v *verifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	v.n += int64(n)
	if v.n > v.size {
		return n, fmt.Errorf("%w: more than %d bytes", ErrCorrupt, v.size)
	}
	if err == io.EOF {
		if got := hex.EncodeToString(v.h.Sum(nil)); v.n != v.size || got != v.want {
			return n, fmt.Errorf("%w: expected %s (%d bytes), got %s (%d bytes)", ErrCorrupt, v.want, v.size, got, v.n)
		}
	}
	return n, err
}

// PutReader stores what r holds and returns its hash and length. The
// content goes to a temporary file as it's read, so it never has to fit
// in memory. If r fails, nothing is stored.
func (s *Store) PutReader(r io.Reader) (string, int64, error) {
	tmp, err := os.CreateTemp(s.tmpDir(), "put-*")
	if err != nil {
		return "", 0, err
	}
	stored := false
	defer func() {
		if !stored {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return "", 0, err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	p, err := s.pathFor(hash)
	if err != nil {
		return "", 0, err
	}
	if _, err := os.Stat(p); err == nil {
		return hash, n, nil
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", 0, err
	}
	stored = true
	syncDir(filepath.Dir(p))
	return hash, n, nil
}

// PutFile stores the file at path and returns its hash and length. A file
// whose content is already stored is only read, not copied.
func (s *Store) PutFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h, n, err := atomicfile.HashReader(f)
	if err != nil || s.Has(h) {
		return h, n, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	return s.PutReader(f)
}

// DiscardPartial throws away what a transfer staged for hash, unless
// another transfer is still appending to it.
func (s *Store) DiscardPartial(hash string) error {
	p, err := s.partialPath(hash)
	if err != nil {
		return err
	}
	if f, err := os.OpenFile(p, os.O_RDWR, 0o600); err == nil {
		locked := lockExclusive(f) == nil
		_ = unlockExclusive(f)
		_ = f.Close()
		if !locked {
			return nil // a transfer is still working on it
		}
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Sweep removes the blobs keep rejects, and what interrupted writes left
// beside them, if they were written before cutoff. It also removes
// staged partials old enough that no transfer is still working on. It
// returns how many files it removed and their size.
func (s *Store) Sweep(keep func(hash string) bool, cutoff time.Time) (removed int, freed int64, err error) {
	err = filepath.WalkDir(filepath.Join(s.Root, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch name := d.Name(); {
		case ValidateHash(name) == nil:
			if keep(name) {
				return nil
			}
		case !strings.HasPrefix(name, atomicfile.TempPrefix):
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		removed++
		freed += info.Size()
		return nil
	})
	if err != nil {
		return removed, freed, err
	}
	premoved, pfreed, perr := s.sweepPartials(cutoff)
	return removed + premoved, freed + pfreed, perr
}

// Remove deletes a blob, if it's stored.
func (s *Store) Remove(hash string) error {
	p, err := s.pathFor(hash)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
