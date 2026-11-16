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
	"time"
)

// ErrBusy means another transfer is already appending to a hash's staged
// content.
var ErrBusy = errors.New("blob: another transfer is staging this content")

// A transfer that stops part way leaves what arrived staged under
// partial/, named by the blob's hash, and the next transfer continues
// from there. Staged content becomes a real blob only when Finish has
// verified that all of it hashes to that name.

func (s *Store) partialDir() string { return filepath.Join(s.Root, "partial") }

func (s *Store) partialPath(hash string) (string, error) {
	if err := ValidateHash(hash); err != nil {
		return "", err
	}
	return filepath.Join(s.partialDir(), hash), nil
}

// PartialSize returns how many bytes of hash a previous interrupted
// transfer staged, or 0.
func (s *Store) PartialSize(hash string) int64 {
	p, err := s.partialPath(hash)
	if err != nil {
		return 0
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

// Resume opens the staged content for hash to append to. Size says how
// many bytes are already staged. Close without Finish keeps them for the
// next transfer; Finish stores the blob once all of it hashes to hash;
// Discard throws the staged content away. Only one Resume for a hash at
// a time; a second one fails with ErrBusy.
func (s *Store) Resume(hash string) (*Partial, error) {
	p, err := s.partialPath(hash)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.partialDir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrBusy, hash)
	}
	st, err := f.Stat()
	if err != nil {
		_ = unlockExclusive(f)
		_ = f.Close()
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, st.Size())); err != nil {
		_ = unlockExclusive(f)
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = unlockExclusive(f)
		_ = f.Close()
		return nil, err
	}
	return &Partial{s: s, hash: hash, f: f, n: st.Size(), h: h}, nil
}

// Partial is content a transfer is staging for one hash.
type Partial struct {
	s    *Store
	hash string
	f    *os.File
	n    int64
	h    gohash.Hash
}

// Size says how many bytes are staged.
func (p *Partial) Size() int64 { return p.n }

// Write appends to the staged content.
func (p *Partial) Write(b []byte) (int, error) {
	n, err := p.f.Write(b)
	p.h.Write(b[:n])
	p.n += int64(n)
	return n, err
}

// Close keeps the staged content for the next transfer. It's safe to
// call after Finish or Discard.
func (p *Partial) Close() error {
	if p.f == nil {
		return nil
	}
	f := p.f
	p.f = nil
	_ = unlockExclusive(f)
	return f.Close()
}

func (p *Partial) letGo() {
	if p.f != nil {
		_ = unlockExclusive(p.f)
		_ = p.f.Close()
		p.f = nil
	}
}

// Discard throws the staged content away.
func (p *Partial) Discard() error {
	f := p.f
	p.letGo()
	if f == nil {
		return nil
	}
	return os.Remove(f.Name())
}

// Finish stores the blob if the staged content hashes to its name. A
// blob the store already has is a success that throws the staged content
// away. A partial that doesn't verify is discarded.
func (p *Partial) Finish() error {
	if p.f == nil {
		return errors.New("blob: the partial is already finished")
	}
	if p.s.Has(p.hash) {
		p.letGo()
		_ = os.Remove(filepath.Join(p.s.partialDir(), p.hash))
		return nil
	}
	if err := p.f.Sync(); err != nil {
		return err
	}
	if got := hex.EncodeToString(p.h.Sum(nil)); got != p.hash {
		p.letGo()
		_ = os.Remove(filepath.Join(p.s.partialDir(), p.hash))
		return fmt.Errorf("%w: expected %s got %s", ErrCorrupt, p.hash, got)
	}
	dst, err := p.s.pathFor(p.hash)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(p.f.Name(), dst); err != nil {
		return err
	}
	p.letGo()
	syncDir(filepath.Dir(dst))
	return nil
}

// sweepPartials removes staged content that is older than cutoff and
// that no transfer is still appending to.
func (s *Store) sweepPartials(cutoff time.Time) (removed int, freed int64, err error) {
	ents, err := os.ReadDir(s.partialDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, e := range ents {
		p := filepath.Join(s.partialDir(), e.Name())
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if f, err := os.OpenFile(p, os.O_RDWR, 0o600); err == nil {
			locked := lockExclusive(f) == nil
			_ = unlockExclusive(f)
			_ = f.Close()
			if !locked {
				continue // a transfer is still working on it
			}
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, freed, err
		}
		removed++
		freed += info.Size()
	}
	return removed, freed, nil
}
