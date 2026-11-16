package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/apps"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/ignore"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
	"github.com/bristlecone-zSuite/zSynk/internal/scan"
)

var (
	// ErrLibraryPath means Synk won't sync a folder at all.
	ErrLibraryPath = errors.New("engine: Synk won't sync this folder")
	// ErrLibraryMissing means a library's folder isn't the one its index
	// describes, usually because a drive isn't mounted or the folder moved.
	ErrLibraryMissing = errors.New("engine: library folder is missing")
	// ErrHeldChanged means the held deletes changed since the caller listed
	// them.
	ErrHeldChanged = errors.New("engine: the held deletes changed; review them again")
	// ErrInvalidLibrary means a library's ID or preset isn't valid.
	ErrInvalidLibrary = errors.New("engine: invalid library")
	// ErrLibraryExists means a library with the ID already syncs another
	// folder.
	ErrLibraryExists = errors.New("engine: library already exists")
	// ErrUnknownLibrary means no library has the ID.
	ErrUnknownLibrary = errors.New("engine: unknown library")
)

// maxIDLen bounds library IDs, which name files in the data folder.
const maxIDLen = 64

// A scan that would delete more than brakeMax files, or more than a tenth of
// the library and at least brakeMin files, holds those deletes until the
// user confirms them. Files that moved don't count: their content is still
// in the library.
const (
	brakeMin = 10
	brakeMax = 50
)

// markerChecks bounds how many known paths ensureMarker looks for.
const markerChecks = 1000

// EnableLibrary adds a library, or updates the one with the same ID and
// folder. Its folder is created if needed and marked with a .synk folder.
// An empty label becomes the preset's name. A nil ShareWith keeps the
// devices an existing library syncs with, and an empty one shares it with
// every paired device.
func (n *Node) EnableLibrary(l config.Library) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	preset, ok := apps.ByID(l.Preset)
	if !ok {
		return fmt.Errorf("%w: unknown preset %q", ErrInvalidLibrary, l.Preset)
	}
	if !ValidLibraryID(l.ID) {
		return fmt.Errorf("%w: ID %q must be 1 to %d lowercase letters, digits, and dashes, starting with a letter or digit", ErrInvalidLibrary, l.ID, maxIDLen)
	}
	if err := n.checkLibraryPath(l); err != nil {
		return err
	}
	l.Path = filepath.Clean(l.Path)
	old := n.Config.LibraryByID(l.ID)
	if old != nil && !samePath(old.Path, l.Path) {
		// Moving a library needs its index to move too; a plain update
		// would report every file as deleted.
		return fmt.Errorf("%w: %q already syncs %s; give the library at %s another ID", ErrLibraryExists, l.ID, old.Path, l.Path)
	}
	share, err := n.shareListLocked(l.ShareWith, old)
	if err != nil {
		return err
	}
	l.ShareWith = share
	if l.Label == "" {
		l.Label = preset.Name
	}
	idx := n.indexes[l.ID]
	if idx == nil {
		idx = reconcile.NewIndex(l.ID, n.Device.DeviceID)
	}
	if err := os.MkdirAll(l.Path, 0o700); err != nil {
		return err
	}
	if err := ensureMarker(l.Path, idx); err != nil {
		return err
	}
	n.Config.UpsertLibrary(l)
	n.indexes[l.ID] = idx
	return n.saveConfigLocked()
}

// shareListLocked checks the devices a library is to sync with, for a
// library that is old before the update. Each must be paired, or already on
// the library's list.
func (n *Node) shareListLocked(ids []string, old *config.Library) ([]string, error) {
	if ids == nil {
		if old == nil {
			return nil, nil
		}
		return slices.Clone(old.ShareWith), nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToUpper(strings.TrimSpace(id))
		if n.Config.PeerByID(id) == nil && (old == nil || !slices.Contains(old.ShareWith, id)) {
			return nil, fmt.Errorf("%w: %q isn't a paired device", ErrInvalidLibrary, id)
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// ValidLibraryID reports whether id can name a library. IDs name files in
// the data folder, so they're lowercase to stay distinct on case-insensitive
// disks.
func ValidLibraryID(id string) bool {
	if id == "" || len(id) > maxIDLen || id[0] == '-' {
		return false
	}
	for _, c := range []byte(id) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func samePath(a, b string) bool {
	ra, rb := safepath.Resolve(a), safepath.Resolve(b)
	return safepath.Within(ra, rb) && safepath.Within(rb, ra)
}

// Libraries returns a copy of the enabled libraries.
func (n *Node) Libraries() []config.Library {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]config.Library, 0, len(n.Config.Libraries))
	for _, l := range n.Config.Libraries {
		out = append(out, cloneLibrary(l))
	}
	return out
}

// Library returns a copy of the library with the ID.
func (n *Node) Library(id string) (config.Library, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if l := n.Config.LibraryByID(id); l != nil {
		return cloneLibrary(*l), true
	}
	return config.Library{}, false
}

func cloneLibrary(l config.Library) config.Library {
	l.ShareWith = slices.Clone(l.ShareWith)
	return l
}

// checkLibraryPath refuses folders that can't be synced safely: cloud
// folders, folders that hold the home folder or Synk's own data, folders
// that overlap another library, and app libraries.
func (n *Node) checkLibraryPath(l config.Library) error {
	if !filepath.IsAbs(l.Path) {
		return fmt.Errorf("%w: %q is not an absolute path", ErrLibraryPath, l.Path)
	}
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s %s", ErrLibraryPath, l.Path, fmt.Sprintf(format, args...))
	}
	p := safepath.Resolve(l.Path)
	if p == filepath.VolumeName(p)+string(filepath.Separator) {
		return refuse("is the whole disk")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if apps.InCloud(home, l.Path) {
			return refuse("is in iCloud Drive or a cloud storage folder, where files are evicted and another sync engine owns them; move it to a local folder first")
		}
		if safepath.Within(safepath.Resolve(home), p) {
			return refuse("holds your whole home folder; pick a project or a Documents subfolder")
		}
	}
	if data := safepath.Resolve(n.DataDir); safepath.Within(p, data) || safepath.Within(data, p) {
		return refuse("overlaps Synk's data folder %s", n.DataDir)
	}
	for _, other := range n.Config.Libraries {
		if other.ID == l.ID {
			continue
		}
		if o := safepath.Resolve(other.Path); safepath.Within(p, o) || safepath.Within(o, p) {
			return refuse("overlaps library %q at %s", other.ID, other.Path)
		}
	}
	if scan.InAppLibrary(filepath.ToSlash(p)) {
		return refuse("is in an app library (Photos, Music, TV, Aperture, or Final Cut), which its app writes continuously")
	}
	return nil
}

// ensureMarker makes sure a library root has its .synk folder. A root
// without one is only marked if it's plausibly the folder idx describes: a
// library with no files yet, or one where a known file is still there.
// Otherwise it's probably an unmounted drive or a moved folder, and a scan
// would report every file as deleted.
func ensureMarker(root string, idx *reconcile.Index) error {
	marker := filepath.Join(root, safepath.SynkDir)
	st, err := os.Lstat(marker)
	switch {
	case err == nil && st.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("engine: %s is not a folder", marker)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if !plausibleRoot(root, idx) {
		return fmt.Errorf("%w: %s has none of the library's files and no %s folder; is the drive mounted, or did the folder move?", ErrLibraryMissing, root, safepath.SynkDir)
	}
	return os.Mkdir(marker, 0o700)
}

func plausibleRoot(root string, idx *reconcile.Index) bool {
	known, checked := false, 0
	for p, e := range idx.Entries {
		if e.Deleted {
			continue
		}
		known = true
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			return true
		}
		if checked++; checked == markerChecks {
			break
		}
	}
	return !known
}

// ignorer is the ignore matcher for lib: the global patterns plus its
// preset's.
func ignorer(lib *config.Library) *ignore.Matcher {
	var extra []string
	if p, ok := apps.ByID(lib.Preset); ok {
		extra = p.Ignore
	}
	return scan.Ignorer(extra)
}

// opsFor is what this device applies to match remote: Reconcile's ops, minus
// those on paths it leaves alone. Those are paths the library ignores here,
// app libraries, and paths the last scan couldn't look at.
func (n *Node) opsFor(lib *config.Library, local, remote *reconcile.Index) []reconcile.Op {
	ig := ignorer(lib)
	var unconfirmed map[string]bool
	if res := n.scans[lib.ID]; res != nil {
		unconfirmed = setOf(res.Unconfirmed)
	}
	skip := func(p string, kind reconcile.Kind) bool {
		return ig.Match(p, kind == reconcile.KindPackage) || scan.InAppLibrary(p) || under(unconfirmed, p)
	}
	var out []reconcile.Op
	for _, op := range reconcile.Reconcile(local, remote) {
		if skip(op.Path, op.KindFile) || op.Kind == reconcile.OpConflict && skip(op.ConflictPath, conflictKind(op)) {
			continue
		}
		out = append(out, op)
	}
	return out
}

// under reports whether p or a folder above it is in set.
func under(set map[string]bool, p string) bool {
	if len(set) == 0 {
		return false
	}
	for ; p != "."; p = path.Dir(p) {
		if set[p] {
			return true
		}
	}
	return set["."]
}

// brake picks the deletes in a scan to hold for confirmation: the ones
// whose content is gone from the library, if there are too many of them.
func brake(prev, next *reconcile.Index, deleted []string, confirmed map[string]bool) []string {
	kept := map[string]bool{}
	for _, e := range next.Entries {
		if !e.Deleted && e.Hash != "" {
			kept[e.Hash] = true
		}
	}
	var lossy []string
	for _, p := range deleted {
		if !confirmed[p] && !kept[prev.Entries[p].Hash] {
			lossy = append(lossy, p)
		}
	}
	if len(lossy) < brakeMin || len(lossy) <= brakeMax && len(lossy)*10 <= liveEntries(prev) {
		return nil
	}
	return lossy
}

func liveEntries(idx *reconcile.Index) int {
	live := 0
	for _, e := range idx.Entries {
		if !e.Deleted {
			live++
		}
	}
	return live
}

// HeldDeletes lists the deletes found in library id that wait for
// confirmation, as of its last scan.
func (n *Node) HeldDeletes(id string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.held[id])
}

// ConfirmDeletes sends the held deletes for library id to peers. If seen
// isn't nil, it must list exactly the deletes being held, so a user can't
// confirm deletes they weren't shown. It returns the confirmed paths.
func (n *Node) ConfirmDeletes(id string, seen []string) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	held, err := n.heldLocked(id)
	if err != nil {
		return nil, err
	}
	if seen != nil && !sameSet(seen, held) {
		return nil, ErrHeldChanged
	}
	if len(held) == 0 {
		return nil, nil
	}
	n.confirmed[id] = setOf(held)
	if _, err := n.scanLocked(id); err != nil {
		return nil, err
	}
	return held, nil
}

// RestoreDeletes puts back the files whose deletes are held for library id,
// from this device's copies of their content. Any it can't put back return
// from a peer at the next sync. It returns the paths it restored or left
// for peers to restore.
func (n *Node) RestoreDeletes(id string) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	held, err := n.heldLocked(id)
	if err != nil || len(held) == 0 {
		return nil, err
	}
	lib := n.Config.LibraryByID(id)
	env, err := apply.Open(lib.Path, lib.ID, n.Store, n.Vault, n.Journal)
	if err != nil {
		return nil, err
	}
	defer env.Close()
	idx := n.indexes[id]
	ops := make([]reconcile.Op, 0, len(held))
	for _, p := range held {
		e := idx.Entries[p]
		// A tombstone that keeps the entry's vector claims no delete: peers'
		// copies are just as new, so they keep them, and this device asks
		// for them back at the next sync if applying here fails.
		gone := e
		gone.Deleted = true
		idx.Entries[p] = gone
		ops = append(ops, reconcile.Op{Kind: reconcile.OpSet, Path: p, KindFile: e.Kind, Hash: e.Hash, Size: e.Size, Vector: e.Vector, Parts: e.Parts, Meta: e.Meta})
	}
	n.held[id] = nil
	_, applyErr := env.Apply(ops, idx)
	if err := n.saveIndex(idx); err != nil {
		return held, errors.Join(applyErr, err)
	}
	return held, applyErr
}

// heldLocked returns the deletes held for library id, scanning it first if
// this process hasn't.
func (n *Node) heldLocked(id string) ([]string, error) {
	if n.Config.LibraryByID(id) == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	if n.scans[id] == nil {
		if _, err := n.scanLocked(id); err != nil {
			return nil, err
		}
	}
	return slices.Clone(n.held[id]), nil
}

func setOf(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return set
}

func sameSet(a, b []string) bool {
	sa, sb := setOf(a), setOf(b)
	if len(sa) != len(sb) {
		return false
	}
	for p := range sa {
		if !sb[p] {
			return false
		}
	}
	return true
}
