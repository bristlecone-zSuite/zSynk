package vault

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/packages"
)

// Retention, unless a Vault says otherwise.
const (
	keepDefault     = 20
	maxAgeDefault   = 30 * 24 * time.Hour
	maxBytesDefault = 10 << 30
)

// tmpGrace is how long a snapshot may take before Prune counts what it's
// writing as left by a crash.
const tmpGrace = time.Hour

const stampFormat = "20060102T150405.000000000Z"

// Hidden names in the vault are work in progress: tmpPrefix for snapshots
// being written, gonePrefix for what Prune is removing.
const (
	tmpPrefix  = ".tmp-"
	gonePrefix = ".gone-"
)

// ErrNoVersion means the vault doesn't have the version asked for.
var ErrNoVersion = errors.New("vault: no such version")

type EntryMeta struct {
	FolderID string    `json:"folderId"`
	Path     string    `json:"path"`
	Hash     string    `json:"hash"`
	Kind     string    `json:"kind"`
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
	Dir      string    `json:"dir"`
}

type Vault struct {
	Root string
	// Keep is how many versions of each path are kept. MaxAge is how long
	// versions and quarantined content are kept, though each path keeps
	// its newest version. MaxBytes bounds what they all take: quarantined
	// content goes first, then the oldest versions, and each path's newest
	// version last.
	Keep     int
	MaxAge   time.Duration
	MaxBytes int64

	// mu keeps Prune from removing a version that's being read, and from
	// removing a directory a snapshot is about to use.
	mu sync.Mutex
}

// Version is a copy the vault keeps of a file or package, as it was before
// Synk replaced or deleted it.
type Version struct {
	// ID tells the path's versions apart.
	ID   string `json:"id"`
	Path string `json:"path"`
	// Kind is "file" or "package".
	Kind string    `json:"kind"`
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
	// Reason is "pre-overwrite" for content that was replaced, and
	// "pre-delete" for content that was deleted.
	Reason string `json:"reason"`
	// Size is how many bytes the copy holds.
	Size int64 `json:"size"`
}

func Open(root string) (*Vault, error) {
	v := &Vault{Root: root, Keep: keepDefault, MaxAge: maxAgeDefault, MaxBytes: maxBytesDefault}
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "quarantine"), 0o700); err != nil {
		return nil, err
	}
	return v, nil
}

// PathDir is where versions of one library path are kept. The path is hashed
// so deep or long library paths never become deep or long vault paths;
// meta.json in each version records the real path.
func (v *Vault) PathDir(folderID, rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return filepath.Join(v.Root, "versions", folderID, hex.EncodeToString(sum[:16]))
}

// SnapshotFile keeps a copy of a file's current content, read from src.
func (v *Vault) SnapshotFile(folderID, rel string, src io.Reader, hash, reason string) (*EntryMeta, error) {
	return v.snapshot(folderID, rel, hash, packages.KindFile, reason, func(content string) error {
		return copyFile(src, content)
	})
}

// SnapshotTree keeps a copy of a package. src is rooted at the package;
// only regular files and directories are copied.
func (v *Vault) SnapshotTree(folderID, rel string, src fs.FS, hash, reason string) (*EntryMeta, error) {
	return v.snapshot(folderID, rel, hash, packages.KindPackage, reason, func(content string) error {
		return copyTree(src, content)
	})
}

// snapshot builds the version in a temporary directory and renames it into
// place, so a version either exists completely or not at all.
func (v *Vault) snapshot(folderID, rel, hash string, kind packages.Kind, reason string, fill func(content string) error) (*EntryMeta, error) {
	parent := v.PathDir(folderID, rel)
	tmp, err := v.mkTemp(parent)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := fill(filepath.Join(tmp, "content")); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now().UTC()
	final := filepath.Join(parent, now.Format(stampFormat))
	for i := 1; exists(final); i++ {
		final = filepath.Join(parent, fmt.Sprintf("%s-%d", now.Format(stampFormat), i))
	}
	meta := &EntryMeta{FolderID: folderID, Path: rel, Hash: hash, Kind: string(kind), At: now, Reason: reason, Dir: final}
	if err := writeMeta(tmp, meta); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	syncDir(parent)
	v.prune(parent)
	return meta, nil
}

// mkTemp makes a directory for a snapshot to fill in parent, making parent
// if needed.
func (v *Vault) mkTemp(parent string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, tmpPrefix)
}

// Versions lists the versions kept of rel in a library, newest first.
func (v *Vault) Versions(folderID, rel string) ([]Version, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	vs, err := versionsIn(v.PathDir(folderID, rel))
	if err != nil {
		return nil, err
	}
	// Two paths could hash to the same directory.
	vs = slices.DeleteFunc(vs, func(ver Version) bool { return ver.Path != rel })
	sortNewestFirst(vs)
	return vs, nil
}

// List lists the versions kept of every path in a library, newest first.
func (v *Vault) List(folderID string) ([]Version, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	dirs, err := subdirs(filepath.Join(v.Root, "versions", folderID))
	if err != nil {
		return nil, err
	}
	out := []Version{}
	for _, d := range dirs {
		vs, err := versionsIn(d)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	sortNewestFirst(out)
	return out, nil
}

// Read calls read with version id of rel and where its content is: a file,
// or a directory for a package. The version stays until read returns.
func (v *Vault) Read(folderID, rel, id string, read func(ver Version, content string) error) error {
	if !versionID(id) {
		return fmt.Errorf("%w: %q", ErrNoVersion, id)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	dir := filepath.Join(v.PathDir(folderID, rel), id)
	ver, err := readVersion(dir)
	if errors.Is(err, fs.ErrNotExist) || err == nil && ver.Path != rel {
		return fmt.Errorf("%w: %s of %s", ErrNoVersion, id, rel)
	}
	if err != nil {
		return err
	}
	return read(ver, filepath.Join(dir, "content"))
}

func readVersion(dir string) (Version, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return Version{}, err
	}
	var m EntryMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Version{}, err
	}
	size, err := sizeOf(filepath.Join(dir, "content"))
	if err != nil {
		return Version{}, err
	}
	return Version{ID: filepath.Base(dir), Path: m.Path, Kind: m.Kind, Hash: m.Hash, At: m.At, Reason: m.Reason, Size: size}, nil
}

// versionsIn reads the versions in a path's directory, leaving out any
// that can't be read.
func versionsIn(dir string) ([]Version, error) {
	names, err := versionNames(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(names))
	for _, name := range names {
		if ver, err := readVersion(filepath.Join(dir, name)); err == nil {
			out = append(out, ver)
		}
	}
	return out, nil
}

// versionNames lists the versions in a path's directory, oldest first.
func versionNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() && versionID(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// ReadDir sorts by name, and names are times.
	return names, nil
}

func sortNewestFirst(vs []Version) {
	slices.SortFunc(vs, func(a, b Version) int {
		return cmp.Or(b.At.Compare(a.At), strings.Compare(a.Path, b.Path), strings.Compare(b.ID, a.ID))
	})
}

// versionID reports whether name is a version's ID: the time it was kept,
// and a number if another version of the path was kept at the same time.
func versionID(name string) bool {
	stamp, n, numbered := strings.Cut(name, "-")
	if _, err := time.Parse(stampFormat, stamp); err != nil {
		return false
	}
	if !numbered {
		return true
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// stampOf is the time in a version's or quarantined item's name.
func stampOf(name string) (time.Time, bool) {
	stamp, _, _ := strings.Cut(name, "-")
	t, err := time.Parse(stampFormat, stamp)
	return t, err == nil
}

// sizeOf is how many bytes the regular files at p hold, p being a file or
// a directory.
func sizeOf(p string) (int64, error) {
	var n int64
	err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	})
	return n, err
}

// Prune removes the versions and quarantined content that retention no
// longer keeps, and what interrupted snapshots left. It returns how many
// versions and quarantined items it removed, and how many bytes of content
// everything it removed held.
func (v *Vault) Prune(now time.Time) (removed int, freed int64, err error) {
	v.mu.Lock()
	doomed, err := v.doomed(now)
	// Renaming is quick, so the lock isn't held while large trees are
	// removed.
	var gone []victim
	for _, d := range doomed {
		if name := filepath.Base(d.dir); !strings.HasPrefix(name, gonePrefix) {
			to := filepath.Join(filepath.Dir(d.dir), gonePrefix+name)
			if rerr := os.Rename(d.dir, to); rerr != nil {
				err = errors.Join(err, rerr)
				continue
			}
			d.dir = to
		}
		gone = append(gone, d)
	}
	v.mu.Unlock()
	for _, d := range gone {
		if rerr := os.RemoveAll(d.dir); rerr != nil {
			err = errors.Join(err, rerr)
			continue
		}
		freed += d.size
		if d.item {
			removed++
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	versions := filepath.Join(v.Root, "versions")
	for _, d := range gone {
		// Folders a path or a library no longer needs; Remove leaves any
		// that aren't empty.
		for p := filepath.Dir(d.dir); strings.HasPrefix(p, versions+string(filepath.Separator)); p = filepath.Dir(p) {
			if os.Remove(p) != nil {
				break
			}
		}
	}
	return removed, freed, err
}

// victim is something Prune removes. item is false for what interrupted
// work left.
type victim struct {
	dir  string
	size int64
	item bool
}

// doomed lists what Prune removes.
func (v *Vault) doomed(now time.Time) ([]victim, error) {
	keep, maxAge, maxBytes := v.Keep, v.MaxAge, v.MaxBytes
	if keep <= 0 {
		keep = keepDefault
	}
	if maxAge <= 0 {
		maxAge = maxAgeDefault
	}
	if maxBytes <= 0 {
		maxBytes = maxBytesDefault
	}
	// What the count and age rules keep, the size rule may remove: first
	// quarantined content, then versions other than each path's newest,
	// then those, oldest first.
	type kept struct {
		victim
		rank int
		at   time.Time
	}
	var out []victim
	var candidates []kept
	var errs []error
	leftover := func(dir string, info fs.FileInfo, tmp bool) {
		if tmp && now.Sub(info.ModTime()) <= tmpGrace {
			return
		}
		size, err := sizeOf(dir)
		errs = append(errs, err)
		out = append(out, victim{dir: dir, size: size})
	}
	libs, err := subdirs(filepath.Join(v.Root, "versions"))
	if err != nil {
		return nil, err
	}
	for _, lib := range libs {
		paths, err := subdirs(lib)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, p := range paths {
			ents, err := os.ReadDir(p)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			var versions []kept
			for _, e := range ents {
				dir := filepath.Join(p, e.Name())
				info, err := e.Info()
				switch {
				case err != nil:
					errs = append(errs, err)
				case strings.HasPrefix(e.Name(), gonePrefix):
					leftover(dir, info, false)
				case strings.HasPrefix(e.Name(), tmpPrefix):
					leftover(dir, info, true)
				case e.IsDir() && versionID(e.Name()):
					at, _ := stampOf(e.Name())
					size, err := sizeOf(filepath.Join(dir, "content"))
					errs = append(errs, err)
					versions = append(versions, kept{victim{dir, size, true}, 1, at})
				}
			}
			for i, ver := range versions {
				newest := i == len(versions)-1
				switch {
				case i < len(versions)-keep, !newest && now.Sub(ver.at) > maxAge:
					out = append(out, ver.victim)
				case newest:
					ver.rank = 2
					candidates = append(candidates, ver)
				default:
					candidates = append(candidates, ver)
				}
			}
		}
	}
	quarantine := filepath.Join(v.Root, "quarantine")
	ents, err := os.ReadDir(quarantine)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, e := range ents {
		dir := filepath.Join(quarantine, e.Name())
		info, err := e.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if strings.HasPrefix(e.Name(), gonePrefix) {
			leftover(dir, info, false)
			continue
		}
		at, ok := stampOf(e.Name())
		if !ok {
			at = info.ModTime()
		}
		size, err := sizeOf(dir)
		errs = append(errs, err)
		if now.Sub(at) > maxAge {
			out = append(out, victim{dir, size, true})
		} else {
			candidates = append(candidates, kept{victim{dir, size, true}, 0, at})
		}
	}

	var total int64
	for _, c := range candidates {
		total += c.size
	}
	slices.SortStableFunc(candidates, func(a, b kept) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), a.at.Compare(b.at))
	})
	for _, c := range candidates {
		if total <= maxBytes {
			break
		}
		out = append(out, c.victim)
		total -= c.size
	}
	return out, errors.Join(errs...)
}

// subdirs lists the directories in dir that aren't work in progress.
func subdirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// Quarantine keeps bytes that failed verification. name is only a label and
// often comes from a peer, so it is reduced to a safe single path component.
func (v *Vault) Quarantine(name string, data []byte, note string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	stamp := time.Now().UTC().Format(stampFormat)
	parent := filepath.Join(v.Root, "quarantine")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, stamp+"-"+quarantineLabel(name)+"-")
	if err != nil {
		return "", err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "payload"), data, 0o600); err != nil {
		return "", err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "note.txt"), []byte(note), 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

func quarantineLabel(name string) string {
	const max = 64
	var b strings.Builder
	for _, r := range name {
		if b.Len() == max {
			break
		}
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}

func writeMeta(dir string, meta *EntryMeta) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "meta.json"), b, 0o600)
}

func (v *Vault) prune(dir string) {
	keep := v.Keep
	if keep <= 0 {
		keep = keepDefault
	}
	versions, err := versionNames(dir)
	if err != nil {
		return
	}
	for i := 0; i < len(versions)-keep; i++ {
		_ = os.RemoveAll(filepath.Join(dir, versions[i]))
	}
}

func copyFile(src io.Reader, dest string) error {
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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

func copyTree(src fs.FS, dest string) error {
	return fs.WalkDir(src, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dest, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := src.Open(rel)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		return copyFile(f, out)
	})
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
