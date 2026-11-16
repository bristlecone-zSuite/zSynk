// Package scan walks a library and reports what it holds, and what it
// couldn't confirm.
package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/ignore"
	"github.com/bristlecone-zSuite/zSynk/internal/packages"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
)

// ErrNoMarker means the library root has no .synk folder. For a library that
// has synced before, that usually means a drive that isn't mounted or a
// folder that moved, and scanning would report every file as deleted.
var ErrNoMarker = errors.New("scan: library has no .synk folder")

// liveLibraries are app databases that look like packages but are written
// continuously. Copying one file by file corrupts it.
var liveLibraries = []string{".photoslibrary", ".photolibrary", ".aplibrary", ".musiclibrary", ".tvlibrary", ".fcpbundle"}

type Options struct {
	// Ignore matches paths that aren't synced. Nil means the global patterns.
	Ignore *ignore.Matcher
	// Settle is how long a package must go unmodified before it's captured.
	// Apps save a package as several files over a few seconds.
	Settle time.Duration
	// Cache, if set, holds what the last scan read, and is updated by this
	// one. Files and packages whose attributes haven't changed since aren't
	// read again.
	Cache *Cache
	// Racy is how long after a change a file is read again by the next
	// scan, whatever its attributes say: a write in the same clock tick as
	// a read can leave them as they were. RacyWindow suits every common
	// file system.
	Racy time.Duration
}

// RacyWindow covers the coarsest file timestamps Synk meets, FAT's two
// seconds.
const RacyWindow = 2 * time.Second

// Cache remembers what each file and package in a library held when a scan
// last read it. It stays on this device.
type Cache struct {
	// Root is the folder the entries were read from; a cache for another
	// one is ignored.
	Root    string            `json:"root"`
	Entries map[string]Cached `json:"entries"`
	changed bool
}

// Changed reports whether the last scan changed the cache, so it's worth
// saving.
func (c *Cache) Changed() bool { return c.changed }

// Cached is what one path held when it was read, and its attributes then.
// For a package, Tree digests the attributes of everything in it.
type Cached struct {
	Stat  fsmeta.Stat `json:"stat,omitzero"`
	Tree  string      `json:"tree,omitempty"`
	Hash  string      `json:"hash"`
	Size  int64       `json:"size"`
	Parts []string    `json:"parts,omitempty"`
}

type Result struct {
	Items []reconcile.ScanItem
	// Placeholders are files whose content is in iCloud, not on this device.
	Placeholders []string
	// Unconfirmed are paths the scan couldn't look at or into. Known entries
	// at or under them must not be deleted.
	Unconfirmed []string
	// Busy are paths that changed while they were read; the next scan
	// retries them.
	Busy    []string
	Skipped []string
	Errors  []string
}

// Ignorer returns the matcher for a library: the global patterns plus extra.
func Ignorer(extra []string) *ignore.Matcher {
	return ignore.Compile(append(ignore.GlobalPatterns(), extra...))
}

// isDataless is replaced in tests; only macOS has dataless files.
var isDataless = datalessFlag

func Library(root string, opts Options, store *blob.Store) (*Result, error) {
	if opts.Ignore == nil {
		opts.Ignore = Ignorer(nil)
	}
	// A symlinked library root is walked as the folder it points to.
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan: not a directory: %s", root)
	}
	if st, err := os.Lstat(filepath.Join(real, safepath.SynkDir)); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNoMarker, root)
	}
	s := &scanner{root: real, opts: opts, store: store, res: &Result{}, pkgIgnore: Ignorer(nil), stubs: map[string]bool{}, seen: map[string]bool{}, start: time.Now()}
	if c := opts.Cache; c != nil {
		s.next = map[string]Cached{}
		if c.Root != real {
			c.Root, c.Entries = real, nil
		}
	}
	if err := filepath.WalkDir(real, s.visit); err != nil {
		return nil, err
	}
	s.finish()
	if c := opts.Cache; c != nil {
		c.changed = s.cacheChanged || len(s.next) != len(c.Entries)
		c.Entries = s.next
	}
	return s.res, nil
}

type scanner struct {
	root  string
	opts  Options
	store *blob.Store
	res   *Result
	// pkgIgnore applies inside packages, where only the global patterns do;
	// apply builds manifests with the same ones.
	pkgIgnore *ignore.Matcher
	// stubs are the paths iCloud placeholders stand for; seen are paths
	// found as real files or folders, which win over a stub.
	stubs map[string]bool
	seen  map[string]bool
	// next becomes the cache: what this scan read or found unchanged.
	next         map[string]Cached
	cacheChanged bool
	start        time.Time
}

func (s *scanner) visit(abs string, d fs.DirEntry, err error) error {
	rel, rerr := filepath.Rel(s.root, abs)
	if rerr != nil {
		return rerr
	}
	rel = filepath.ToSlash(rel)
	if err != nil {
		if rel == "." {
			return fmt.Errorf("scan: reading %s: %w", s.root, err)
		}
		s.unconfirmed(rel, err.Error())
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if rel == "." {
		return nil
	}
	name := d.Name()
	isDir := d.IsDir()
	if isDir && safepath.IsSynkDir(name) {
		return fs.SkipDir
	}
	if target, ok := placeholderTarget(rel); ok && !isDir {
		s.stub(rel, target)
		return nil
	}
	if s.opts.Ignore.Match(rel, isDir) {
		return skipDir(isDir)
	}
	if safepath.Check(rel) != nil {
		s.skip(rel, "unsupported name")
		return skipDir(isDir)
	}
	if d.Type()&fs.ModeSymlink != 0 {
		s.skip(rel, "symlink")
		return nil
	}
	info, err := d.Info()
	if err != nil {
		s.unconfirmed(rel, err.Error())
		return skipDir(isDir)
	}
	s.seen[rel] = true
	if isDir {
		switch {
		case isLiveLibrary(name):
			s.skip(rel, "app library; Synk never syncs these")
			return fs.SkipDir
		case isDataless(info):
			s.skip(rel, "not downloaded")
			return fs.SkipDir
		case packages.IsPackageName(name):
			s.pkg(abs, rel)
			return fs.SkipDir
		}
		return nil
	}
	switch {
	case !info.Mode().IsRegular():
		s.skip(rel, "not a regular file")
	case isDataless(info):
		s.res.Placeholders = append(s.res.Placeholders, rel)
		s.incomplete(rel, reconcile.KindFile)
	default:
		s.file(abs, rel, info)
	}
	return nil
}

func (s *scanner) file(abs, rel string, before fs.FileInfo) {
	st := fsmeta.FromInfo(before)
	if c, ok := s.reuse(rel, func(c Cached) bool { return c.Tree == "" && c.Stat == st }); ok {
		s.add(reconcile.ScanItem{Path: rel, Kind: reconcile.KindFile, Hash: c.Hash, Size: c.Size, Exec: fsmeta.Exec(st.Mode), MTime: st.MTime})
		return
	}
	h, n, err := s.store.PutFile(abs)
	if err != nil {
		s.res.Errors = append(s.res.Errors, rel+": "+err.Error())
		s.incomplete(rel, reconcile.KindFile)
		return
	}
	after, err := os.Lstat(abs)
	if err != nil || fsmeta.FromInfo(after) != st || n != after.Size() {
		s.res.Busy = append(s.res.Busy, rel)
		s.incomplete(rel, reconcile.KindFile)
		return
	}
	s.remember(rel, Cached{Stat: st, Hash: h, Size: n}, changedAt(st))
	s.add(reconcile.ScanItem{Path: rel, Kind: reconcile.KindFile, Hash: h, Size: n, Exec: fsmeta.Exec(st.Mode), MTime: st.MTime})
}

// reuse returns what rel held when it was last read, if same says it hasn't
// changed since and the store still has its content.
func (s *scanner) reuse(rel string, same func(Cached) bool) (Cached, bool) {
	if s.opts.Cache == nil {
		return Cached{}, false
	}
	c, ok := s.opts.Cache.Entries[rel]
	if !ok || !same(c) || !s.store.Has(c.Hash) {
		return Cached{}, false
	}
	for _, p := range c.Parts {
		if !s.store.Has(p) {
			return Cached{}, false
		}
	}
	s.next[rel] = c
	return c, true
}

// remember caches what rel held, unless it changed within the racy window
// before this scan, when its attributes can't vouch for its content.
func (s *scanner) remember(rel string, c Cached, changed time.Time) {
	if s.opts.Cache == nil || !changed.Before(s.start.Add(-s.opts.Racy)) {
		return
	}
	s.next[rel] = c
	s.cacheChanged = true
}

// changedAt is when a file last changed. The ctime is set by the system on
// every change, so it's used where there is one, rather than the mtime,
// which apps can set to anything.
func changedAt(st fsmeta.Stat) time.Time {
	if st.CTime != 0 {
		return time.Unix(0, st.CTime)
	}
	return time.Unix(0, st.MTime)
}

// pkg captures a package as one unit, or not at all: a package with parts
// that aren't downloaded, that changed recently, or that changed while it
// was read is recorded as Incomplete and retried by the next scan.
func (s *scanner) pkg(abs, rel string) {
	item, busy, err := s.capturePackage(abs, rel)
	switch {
	case err != nil:
		s.res.Errors = append(s.res.Errors, rel+": "+err.Error())
		s.incomplete(rel, reconcile.KindPackage)
	case busy:
		s.res.Busy = append(s.res.Busy, rel)
		s.incomplete(rel, reconcile.KindPackage)
	default:
		s.add(item)
	}
}

func (s *scanner) capturePackage(abs, rel string) (reconcile.ScanItem, bool, error) {
	r, err := os.OpenRoot(abs)
	if err != nil {
		return reconcile.ScanItem{}, false, err
	}
	defer r.Close()
	fsys := r.FS()
	before, newest, mtime, err := s.inspect(fsys)
	if err != nil {
		return reconcile.ScanItem{}, false, err
	}
	if age := time.Since(newest); age < s.opts.Settle && age > -s.opts.Settle {
		return reconcile.ScanItem{}, true, nil
	}
	tree := atomicfile.HashBytes([]byte(before))
	if c, ok := s.reuse(rel, func(c Cached) bool { return c.Tree == tree }); ok {
		return reconcile.ScanItem{Path: rel, Kind: reconcile.KindPackage, Hash: c.Hash, Size: c.Size, Parts: c.Parts, MTime: mtime}, false, nil
	}
	man, raw, err := packages.BuildManifestFS(fsys, s.pkgIgnore)
	if err != nil {
		return reconcile.ScanItem{}, false, err
	}
	item := reconcile.ScanItem{Path: rel, Kind: reconcile.KindPackage, Hash: packages.HashManifest(raw), MTime: mtime}
	for _, f := range man.Files {
		item.Size += f.Size
		item.Parts = append(item.Parts, f.Hash)
		if s.store.Has(f.Hash) {
			continue
		}
		if h, err := s.putPart(fsys, f.RelPath); err != nil {
			return reconcile.ScanItem{}, false, err
		} else if h != f.Hash {
			return reconcile.ScanItem{}, true, nil
		}
	}
	if _, err := s.store.Put(raw); err != nil {
		return reconcile.ScanItem{}, false, err
	}
	after, _, _, err := s.inspect(fsys)
	if err != nil {
		return reconcile.ScanItem{}, false, err
	}
	if after != before {
		return reconcile.ScanItem{}, true, nil
	}
	s.remember(rel, Cached{Tree: tree, Hash: item.Hash, Size: item.Size, Parts: item.Parts}, newest)
	return item, false, nil
}

func (s *scanner) putPart(fsys fs.FS, rel string) (string, error) {
	f, err := fsys.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, _, err := s.store.PutReader(f)
	return h, err
}

// inspect summarizes a package's tree so a change during capture can be
// detected, and finds its newest change and its newest modification time,
// which is the package's. It fails if any part isn't on this device.
func (s *scanner) inspect(fsys fs.FS) (sig string, newest time.Time, mtime int64, err error) {
	var b strings.Builder
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if _, ok := placeholderTarget(p); ok && !d.IsDir() {
			return fmt.Errorf("%s isn't downloaded from iCloud", p)
		}
		if p != "." && s.pkgIgnore.Match(p, d.IsDir()) {
			return skipDir(d.IsDir())
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if isDataless(fi) {
			return fmt.Errorf("%s isn't downloaded", p)
		}
		st := fsmeta.FromInfo(fi)
		fmt.Fprintf(&b, "%s\x00%+v\n", p, st)
		changed := time.Unix(0, st.CTime)
		if st.CTime == 0 {
			changed = fi.ModTime()
		}
		if changed.After(newest) {
			newest = changed
		}
		mtime = max(mtime, st.MTime)
		return nil
	})
	return b.String(), newest, mtime, err
}

// stub records an iCloud placeholder for target.
func (s *scanner) stub(rel, target string) {
	if safepath.Check(target) != nil {
		s.skip(target, "unsupported name")
		return
	}
	if s.opts.Ignore.Match(target, packages.IsPackageName(path.Base(target))) {
		return
	}
	s.res.Placeholders = append(s.res.Placeholders, rel)
	s.stubs[target] = true
}

func (s *scanner) finish() {
	targets := make([]string, 0, len(s.stubs))
	for t := range s.stubs {
		if !s.seen[t] {
			targets = append(targets, t)
		}
	}
	sort.Strings(targets)
	for _, t := range targets {
		kind := reconcile.KindFile
		if packages.IsPackageName(path.Base(t)) {
			kind = reconcile.KindPackage
		}
		s.incomplete(t, kind)
	}
}

func (s *scanner) add(it reconcile.ScanItem) {
	s.seen[it.Path] = true
	s.res.Items = append(s.res.Items, it)
}

func (s *scanner) incomplete(rel string, kind reconcile.Kind) {
	s.add(reconcile.ScanItem{Path: rel, Kind: kind, Incomplete: true})
}

func (s *scanner) skip(rel, reason string) {
	s.res.Skipped = append(s.res.Skipped, rel+" ("+reason+")")
	s.res.Unconfirmed = append(s.res.Unconfirmed, rel)
}

func (s *scanner) unconfirmed(rel, why string) {
	s.res.Errors = append(s.res.Errors, rel+": "+why)
	s.res.Unconfirmed = append(s.res.Unconfirmed, rel)
}

// placeholderTarget maps an iCloud placeholder to the path it stands for:
// dir/.name.icloud stands for dir/name.
func placeholderTarget(rel string) (string, bool) {
	dir, name := path.Split(rel)
	if !strings.HasSuffix(strings.ToLower(name), ".icloud") {
		return "", false
	}
	base := strings.TrimPrefix(name[:len(name)-len(".icloud")], ".")
	if base == "" {
		return "", false
	}
	return dir + base, true
}

func isLiveLibrary(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, l := range liveLibraries {
		if ext == l {
			return true
		}
	}
	return false
}

// InAppLibrary reports whether the slash-separated path p is, or is inside,
// an app library such as a Photos library. Synk never reads or writes
// those.
func InAppLibrary(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if isLiveLibrary(part) {
			return true
		}
	}
	return false
}

func skipDir(isDir bool) error {
	if isDir {
		return fs.SkipDir
	}
	return nil
}
