package packages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/ignore"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
)

// Document packages that must be copied as a single unit. Syncing their
// interior files independently is a well-known way to corrupt iWork and
// textbundle documents.
var DefaultExtensions = []string{
	".pages",
	".key",
	".numbers",
	".textbundle",
	".rtfd",
	".ulysses",
	".ulgroup",
	".scriv",
	".band",
}

type Kind string

const (
	KindFile    Kind = "file"
	KindPackage Kind = "package"
	KindDir     Kind = "dir"
)

type FilePart struct {
	RelPath string `json:"path"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
}

type Manifest struct {
	Kind  Kind       `json:"kind"`
	Files []FilePart `json:"files"`
}

func IsPackageName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range DefaultExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// HashFile hashes a regular file without reading it all into memory.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return atomicfile.HashReader(f)
}

// BuildManifest walks a package directory and returns a canonical manifest.
func BuildManifest(root string, ig *ignore.Matcher) (*Manifest, []byte, error) {
	return BuildManifestFS(os.DirFS(root), ig)
}

// BuildManifestFS is BuildManifest over fsys, which is rooted at the package.
// Only regular files are parts; symlinks and special files are skipped. A part
// whose name a peer would refuse fails the whole manifest, because a package
// synced without one of its files is a torn package.
func BuildManifestFS(fsys fs.FS, ig *ignore.Matcher) (*Manifest, []byte, error) {
	var files []FilePart
	err := fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if ig != nil && ig.Match(rel, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if err := safepath.Check(rel); err != nil {
			return fmt.Errorf("packages: part %q: %w", rel, err)
		}
		f, err := fsys.Open(rel)
		if err != nil {
			return err
		}
		h, n, err := atomicfile.HashReader(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		files = append(files, FilePart{RelPath: rel, Hash: h, Size: n})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	m := &Manifest{Kind: KindPackage, Files: files}
	raw, err := Canonical(m)
	if err != nil {
		return nil, nil, err
	}
	return m, raw, nil
}

func Canonical(m *Manifest) ([]byte, error) {
	return json.Marshal(m)
}

func HashManifest(raw []byte) string {
	return atomicfile.HashBytes(raw)
}

// ParseManifest decodes and validates manifest bytes, which may have come
// from a peer.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("packages: manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks that every part is a safe relative path with a valid hash
// and that parts are listed once each, in the sorted order BuildManifest uses.
func (m *Manifest) Validate() error {
	if m.Kind != KindPackage {
		return fmt.Errorf("packages: manifest kind is %q", m.Kind)
	}
	for i, f := range m.Files {
		if err := safepath.Check(f.RelPath); err != nil {
			return fmt.Errorf("packages: part %q: %w", f.RelPath, err)
		}
		if err := blob.ValidateHash(f.Hash); err != nil {
			return fmt.Errorf("packages: part %q: %w", f.RelPath, err)
		}
		if f.Size < 0 {
			return fmt.Errorf("packages: part %q has a negative size", f.RelPath)
		}
		if i > 0 && m.Files[i-1].RelPath >= f.RelPath {
			return fmt.Errorf("packages: parts are not sorted and unique at %q", f.RelPath)
		}
	}
	return nil
}

// BlobOpen streams the content for hash. The reader must fail before EOF
// if the content doesn't match; blob.Store.Open does.
type BlobOpen func(hash string) (io.ReadCloser, int64, error)

// Materialize writes the package described by m into dir, which must be
// empty. dir confines every write, so a part can't land outside it.
func Materialize(dir *os.Root, m *Manifest, open BlobOpen) error {
	if err := m.Validate(); err != nil {
		return err
	}
	dirs := map[string]bool{".": true}
	for _, f := range m.Files {
		if d := path.Dir(f.RelPath); d != "." {
			if err := dir.MkdirAll(d, 0o700); err != nil {
				return err
			}
			for ; d != "."; d = path.Dir(d) {
				dirs[d] = true
			}
		}
		if err := writePart(dir, f, open); err != nil {
			return fmt.Errorf("packages: part %q: %w", f.RelPath, err)
		}
	}
	for d := range dirs {
		if err := SyncDir(dir, d); err != nil {
			return err
		}
	}
	return nil
}

func writePart(dir *os.Root, f FilePart, open BlobOpen) error {
	src, size, err := open(f.Hash)
	if err != nil {
		return err
	}
	defer src.Close()
	if size != f.Size {
		return ErrManifestMismatch
	}
	out, err := dir.OpenFile(f.RelPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, src)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// SyncDir fsyncs a directory inside root so new entries in it are durable.
func SyncDir(root *os.Root, name string) error {
	d, err := root.Open(name)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

var ErrManifestMismatch = errors.New("packages: content does not match manifest")
