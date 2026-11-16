package engine

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
	"github.com/bristlecone-zSuite/zSynk/internal/journal"
	"github.com/bristlecone-zSuite/zSynk/internal/lockfile"
	"github.com/bristlecone-zSuite/zSynk/internal/packages"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/scan"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

// Node is a complete Synk instance for one device.
type Node struct {
	mu      sync.Mutex
	DataDir string
	Device  *identity.Device
	Config  *config.File
	Store   *blob.Store
	Vault   *vault.Vault
	Journal *journal.Log
	// PackageSettle is how long a package must go unmodified before a scan
	// captures it.
	PackageSettle time.Duration
	// RacyWindow is how recently a file may have changed and still be read
	// again by the next scan; see scan.Options.
	RacyWindow time.Duration
	// Log gets what goes wrong, and what the node changes. It discards
	// everything unless it's replaced before the node is used.
	Log *slog.Logger
	// tasks is what the node is doing now.
	tasks tasks
	// idleTimeout ends a session with another device once it has sent and
	// accepted nothing for that long. Sessions have no overall limit, so a
	// large file takes as long as the network needs.
	idleTimeout time.Duration
	indexes     map[string]*reconcile.Index
	// caches are the libraries' scan caches; saved are the index files as
	// last written, and gens count the times each index changed.
	caches map[string]*scan.Cache
	saved  map[string][]byte
	gens   map[string]uint64
	synced map[syncPair]syncMark
	// pins count the sessions in progress that need each blob.
	pins      map[string]int
	recovered map[string]bool
	// scans holds each library's last scan; held and confirmed are the
	// deletes waiting for the user and the ones the user let through.
	scans     map[string]*scan.Result
	held      map[string][]string
	confirmed map[string]map[string]bool
	scanErrs  map[string]string
	boundAddr string
	// offer is the pairing offer waiting for a device, if any.
	offer *pairing.Pending
	// cert carries the device's key in every connection with a peer.
	cert tls.Certificate
	lock *lockfile.Lock
	// folds caches whether each library's volume folds case, and FoldProbe
	// replaces the probe in tests. foldsMu guards folds, because the sync
	// paths that read it already hold mu.
	folds     map[string]bool
	foldsMu   sync.Mutex
	FoldProbe func(dir string) bool
	// subs are the event streams apps watch; eventsMu guards them.
	eventsMu sync.Mutex
	subs     map[chan<- Event]bool
}

// ErrInUse means another Node, usually `synk serve` in another process,
// has the data folder open.
var ErrInUse = errors.New("engine: another Synk process is using the data folder")

// LockPath is the file whose lock gives one Node at a time the data folder.
func LockPath(dataDir string) string {
	return filepath.Join(dataDir, "lock")
}

// Open opens the node whose data is in dataDir. Only one Node may have a
// data folder open at a time, in any process, until it's closed.
func Open(dataDir, deviceName string) (*Node, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	lock, err := lockfile.Acquire(LockPath(dataDir))
	if errors.Is(err, lockfile.ErrLocked) {
		if pid := lockfile.Holder(LockPath(dataDir)); pid != 0 {
			return nil, fmt.Errorf("%w (%s, process %d)", ErrInUse, dataDir, pid)
		}
		return nil, fmt.Errorf("%w (%s)", ErrInUse, dataDir)
	}
	if err != nil {
		return nil, fmt.Errorf("engine: locking %s: %w", dataDir, err)
	}
	n, err := open(dataDir, deviceName)
	if err != nil {
		_ = lock.Release()
		return nil, err
	}
	n.lock = lock
	return n, nil
}

// Close releases the data folder. The node must not be used afterwards.
func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	err := n.lock.Release()
	n.lock = nil
	return err
}

func open(dataDir, deviceName string) (*Node, error) {
	dev, key, err := identity.LoadOrCreate(dataDir, deviceName)
	if err != nil {
		return nil, err
	}
	cert, err := transport.Certificate(key)
	if err != nil {
		return nil, err
	}
	// An unreadable offer is one nobody can use; a new offer replaces it.
	offer, _ := pairing.LoadPending(dataDir)
	cfg, err := config.Load(dataDir)
	if err != nil {
		return nil, err
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = deviceName
		_ = config.Save(dataDir, cfg)
	}
	store, err := blob.New(filepath.Join(dataDir, "blobs"))
	if err != nil {
		return nil, err
	}
	v, err := vault.Open(filepath.Join(dataDir, "vault"))
	if err != nil {
		return nil, err
	}
	j, err := journal.Open(filepath.Join(dataDir, "journal"))
	if err != nil {
		return nil, err
	}
	n := &Node{
		DataDir:       dataDir,
		Device:        dev,
		Config:        cfg,
		Store:         store,
		Vault:         v,
		Journal:       j,
		PackageSettle: 10 * time.Second,
		RacyWindow:    scan.RacyWindow,
		Log:           slog.New(slog.DiscardHandler),
		idleTimeout:   5 * time.Minute,
		indexes:       map[string]*reconcile.Index{},
		caches:        map[string]*scan.Cache{},
		saved:         map[string][]byte{},
		gens:          map[string]uint64{},
		synced:        map[syncPair]syncMark{},
		pins:          map[string]int{},
		recovered:     map[string]bool{},
		scans:         map[string]*scan.Result{},
		held:          map[string][]string{},
		confirmed:     map[string]map[string]bool{},
		scanErrs:      map[string]string{},
		folds:         map[string]bool{},
		offer:         offer,
		cert:          cert,
	}
	if err := n.loadIndexes(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Node) SaveConfig() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.saveConfigLocked()
}

func (n *Node) saveConfigLocked() error {
	return config.Save(n.DataDir, n.Config)
}

func (n *Node) indexPath(folderID string) string {
	return filepath.Join(n.DataDir, "indexes", folderID+".json")
}

func (n *Node) loadIndexes() error {
	dir := filepath.Join(n.DataDir, "indexes")
	_ = os.MkdirAll(dir, 0o700)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		idx := reconcile.NewIndex("", "")
		if err := json.Unmarshal(b, idx); err != nil {
			return err
		}
		if idx.Entries == nil {
			idx.Entries = map[string]reconcile.Entry{}
		}
		n.indexes[idx.FolderID] = idx
	}
	return nil
}

// saveIndex writes a library's index if it changed since it was last
// written, and counts the change.
func (n *Node) saveIndex(idx *reconcile.Index) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if bytes.Equal(b, n.saved[idx.FolderID]) {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(n.DataDir, "indexes"), 0o700); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(n.indexPath(idx.FolderID), b, 0o600); err != nil {
		return err
	}
	n.saved[idx.FolderID] = b
	n.gens[idx.FolderID]++
	return nil
}

func (n *Node) cachePath(id string) string {
	return filepath.Join(n.DataDir, "cache", id+".json")
}

// scanCache returns a library's scan cache, read from disk the first time.
// A cache that can't be read is rebuilt by reading the files again.
func (n *Node) scanCache(id string) *scan.Cache {
	if c := n.caches[id]; c != nil {
		return c
	}
	c := &scan.Cache{}
	if b, err := os.ReadFile(n.cachePath(id)); err == nil && json.Unmarshal(b, c) != nil {
		c = &scan.Cache{}
	}
	n.caches[id] = c
	return c
}

func (n *Node) saveCache(id string, c *scan.Cache) error {
	if err := os.MkdirAll(filepath.Dir(n.cachePath(id)), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(n.cachePath(id), b, 0o600)
}

// execBits is replaced in tests, whose file systems all keep the bits.
var execBits = fsmeta.ExecBits

func (n *Node) ScanLibrary(id string) (*scan.Result, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.scanLocked(id)
}

func (n *Node) scanLocked(id string) (res *scan.Result, err error) {
	lib := n.Config.LibraryByID(id)
	if lib == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	defer n.tasks.begin(Task{Library: id, Doing: Scanning})()
	defer func() {
		was := n.scanErrs[id]
		switch {
		case err != nil:
			n.scanErrs[id] = err.Error()
			if was != err.Error() {
				n.Log.Warn("scan failed", "library", id, "err", err)
			}
		case was != "":
			delete(n.scanErrs, id)
			n.Log.Info("scanning again", "library", id)
		}
	}()
	prev := n.indexes[id]
	if prev == nil {
		prev = reconcile.NewIndex(id, n.Device.DeviceID)
	}
	if err := ensureMarker(lib.Path, prev); err != nil {
		return nil, err
	}
	if !n.recovered[id] {
		if err := n.recoverLocked(lib); err != nil {
			return nil, err
		}
	}
	ig := ignorer(lib)
	cache := n.scanCache(id)
	res, err = scan.Library(lib.Path, scan.Options{Ignore: ig, Settle: n.PackageSettle, Cache: cache, Racy: n.RacyWindow}, n.Store)
	if err != nil {
		return nil, err
	}
	if cache.Changed() {
		// Without its cache, the next scan reads the files again.
		_ = n.saveCache(id, cache)
	}
	opts := reconcile.AdvanceOptions{Unconfirmed: res.Unconfirmed, Ignored: ig.Match, NoExec: !execBits(lib.Path)}
	next, deleted := reconcile.Advance(prev, n.Device.DeviceID, res.Items, opts)
	held := brake(prev, next, deleted, n.confirmed[id])
	if held != nil {
		opts.Hold = setOf(held)
		next, _ = reconcile.Advance(prev, n.Device.DeviceID, res.Items, opts)
	}
	if len(held) > 0 && !sameSet(held, n.held[id]) {
		n.Log.Warn("holding deletes until they're confirmed", "library", id, "count", len(held))
		n.publish("deletes", id, "", "warning", fmt.Sprint(len(held))+" deletes are held until you confirm or restore them")
	}
	if last := n.scans[id]; len(res.Errors) > 0 && (last == nil || !slices.Equal(res.Errors, last.Errors)) {
		n.Log.Warn("couldn't read some files", "library", id, "count", len(res.Errors), "first", res.Errors[0])
		n.publish("scan", id, "", "warning", "can't read "+fmt.Sprint(len(res.Errors))+" files; the first is "+res.Errors[0])
	}
	next.FolderID = id
	n.indexes[id] = next
	n.scans[id] = res
	n.held[id] = held
	if err := n.saveIndex(next); err != nil {
		return res, err
	}
	delete(n.confirmed, id)
	return res, nil
}

// recoverLocked finishes or rolls back the library's interrupted applies.
// It runs before the library's first scan in this process.
func (n *Node) recoverLocked(lib *config.Library) error {
	env, err := apply.Open(lib.Path, lib.ID, n.Store, n.Vault, n.Journal)
	if err != nil {
		return err
	}
	defer env.Close()
	if err := env.Recover(); err != nil {
		return fmt.Errorf("engine: recovering %s: %w", lib.ID, err)
	}
	n.recovered[lib.ID] = true
	return nil
}

func (n *Node) Index(id string) *reconcile.Index {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.indexes[id]
}

func (n *Node) ExportIndex(id string) (*reconcile.Index, error) {
	idx, _, err := n.exportIndex(id)
	return idx, err
}

// exportIndex scans a library and returns a copy of its index, and how
// many times the index had changed.
func (n *Node) exportIndex(id string) (*reconcile.Index, uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, err := n.scanLocked(id); err != nil {
		return nil, 0, err
	}
	src := n.indexes[id]
	cp := reconcile.NewIndex(src.FolderID, n.Device.DeviceID)
	for k, e := range src.Entries {
		cp.Entries[k] = e
	}
	return cp, n.gens[id], nil
}

// Wants lists the content this node lacks to match remote, given local,
// its index as sent to the peer. Each entry says how many bytes of the
// blob this node already holds staged from a transfer that stopped part
// way, so the peer sends only what's missing. Until release is called,
// the store keeps all the content those changes need.
func (n *Node) Wants(local, remote *reconcile.Index) ([]wantEntry, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var ops []reconcile.Op
	if lib := n.Config.LibraryByID(remote.FolderID); lib != nil {
		ops = n.opsFor(lib, local, n.remoteFor(lib, local, remote))
	}
	needed := neededBlobs(ops)
	for _, h := range needed {
		n.pins[h]++
	}
	offered := referenced(remote)
	var wants []wantEntry
	for _, h := range needed {
		limit, ok := offered[h]
		if !ok || n.Store.Has(h) {
			continue
		}
		off := n.Store.PartialSize(h)
		if off >= limit {
			off = 0
		}
		wants = append(wants, wantEntry{Hash: h, Offset: off})
	}
	return wants, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, h := range needed {
			if n.pins[h]--; n.pins[h] == 0 {
				delete(n.pins, h)
			}
		}
	}
}

// MissingBlobs lists the content needed to apply ops that this node lacks.
// Only content remote references is listed, because that's all a peer
// serves.
func (n *Node) MissingBlobs(ops []reconcile.Op, remote *reconcile.Index) []string {
	return n.missingBlobs(neededBlobs(ops), remote)
}

func (n *Node) missingBlobs(needed []string, remote *reconcile.Index) []string {
	offered := referenced(remote)
	var hashes []string
	for _, h := range needed {
		if _, ok := offered[h]; ok && !n.Store.Has(h) {
			hashes = append(hashes, h)
		}
	}
	return hashes
}

// neededBlobs lists the content ops need, once each.
func neededBlobs(ops []reconcile.Op) []string {
	var out []string
	seen := map[string]bool{}
	add := func(hs ...string) {
		for _, h := range hs {
			if h != "" && !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	for _, op := range ops {
		switch op.Kind {
		case reconcile.OpSet:
			add(op.Hash)
			add(op.Parts...)
		case reconcile.OpConflict:
			add(op.Hash, op.ConflictHash)
			add(op.Parts...)
			add(op.ConflictParts...)
		}
	}
	return out
}

// gcGrace keeps content that no index references for a while after it's
// stored, so content a sync received but couldn't apply yet isn't sent
// again by the next one.
const gcGrace = time.Hour

// CollectGarbage removes content from the store that no library's index
// references and no session in progress needs, and what interrupted writes
// left there. It returns how many files it removed and their size.
func (n *Node) CollectGarbage() (removed int, freed int64, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	keep := map[string]bool{}
	for _, idx := range n.indexes {
		for _, e := range idx.Entries {
			if e.Deleted {
				continue
			}
			keep[e.Hash] = true
			for _, p := range e.Parts {
				keep[p] = true
			}
		}
	}
	return n.Store.Sweep(func(h string) bool { return keep[h] || n.pins[h] > 0 }, time.Now().Add(-gcGrace))
}

func (n *Node) PutBlob(data []byte) (string, error) {
	return n.Store.Put(data)
}

func (n *Node) GetBlob(hash string) ([]byte, error) {
	return n.Store.Get(hash)
}

// PullFrom applies remote's index onto this node for one library.
func (n *Node) PullFrom(remote *reconcile.Index, get func(hash string) ([]byte, error)) error {
	if remote == nil {
		return fmt.Errorf("engine: no index to pull")
	}
	remote, _, err := checkRemote(remote, remote.FolderID)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	lib := n.Config.LibraryByID(remote.FolderID)
	if lib == nil {
		return fmt.Errorf("engine: library %s not enabled", remote.FolderID)
	}
	if _, err := n.scanLocked(lib.ID); err != nil {
		return err
	}
	local := n.indexes[lib.ID]
	remote = n.remoteFor(lib, local, remote)
	ops := n.opsFor(lib, local, remote)
	if len(ops) == 0 {
		return nil
	}
	defer n.tasks.begin(Task{Library: lib.ID, Doing: Applying})()
	var ready []reconcile.Op
	var errs []error
	for _, op := range ops {
		if err := n.ensureOp(op, get); err != nil {
			// Skipped ops stay different, so the next sync retries them.
			errs = append(errs, fmt.Errorf("%s: %w", op.Path, err))
			continue
		}
		ready = append(ready, op)
	}
	env, err := apply.Open(lib.Path, lib.ID, n.Store, n.Vault, n.Journal)
	if err != nil {
		return err
	}
	defer env.Close()
	deferred, applyErr := env.Apply(ready, local)
	if applyErr == nil && len(ready) > len(deferred) {
		n.Log.Info("applied changes", "library", lib.ID, "changes", len(ready)-len(deferred))
	}
	errs = append(errs, applyErr)
	// Ops that did apply are on disk; the index must say so even if others failed.
	if err := n.saveIndex(local); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ensureOp stores the content op needs. Nothing in a conflict is applied
// unless both versions are available.
func (n *Node) ensureOp(op reconcile.Op, get func(string) ([]byte, error)) error {
	switch op.Kind {
	case reconcile.OpSet:
		return n.ensureContent(op.KindFile, op.Hash, get)
	case reconcile.OpConflict:
		if err := n.ensureContent(op.KindFile, op.Hash, get); err != nil {
			return err
		}
		return n.ensureContent(conflictKind(op), op.ConflictHash, get)
	default:
		return nil
	}
}

func conflictKind(op reconcile.Op) reconcile.Kind {
	if op.ConflictKind != "" {
		return op.ConflictKind
	}
	return op.KindFile
}

func (n *Node) ensureContent(kind reconcile.Kind, hash string, get func(string) ([]byte, error)) error {
	if kind == reconcile.KindPackage {
		return n.ensurePackageParts(hash, get)
	}
	return n.ensureBlob(hash, get)
}

func (n *Node) ensureBlob(hash string, get func(string) ([]byte, error)) error {
	if hash == "" {
		return nil
	}
	if err := blob.ValidateHash(hash); err != nil {
		return err
	}
	if n.Store.Has(hash) {
		return nil
	}
	data, err := get(hash)
	if err != nil {
		return err
	}
	if atomicfile.HashBytes(data) != hash {
		_, _ = n.Vault.Quarantine(hash, data, "peer blob hash mismatch")
		return fmt.Errorf("engine: peer blob hash mismatch for %s", hash)
	}
	_, err = n.Store.Put(data)
	return err
}

func (n *Node) ensurePackageParts(manifestHash string, get func(string) ([]byte, error)) error {
	if err := n.ensureBlob(manifestHash, get); err != nil {
		return err
	}
	raw, err := n.Store.Get(manifestHash)
	if err != nil {
		return err
	}
	man, err := packages.ParseManifest(raw)
	if err != nil {
		return err
	}
	for _, f := range man.Files {
		if err := n.ensureBlob(f.Hash, get); err != nil {
			return err
		}
	}
	return nil
}

// Pair makes a and b trust each other without a network.
func Pair(a, b *Node) error {
	now := time.Now().UTC()
	if err := a.addPeer(config.Peer{DeviceID: b.Device.DeviceID, Name: b.Device.Name, PublicKey: b.Device.PublicKey, PairedAt: now}); err != nil {
		return err
	}
	return b.addPeer(config.Peer{DeviceID: a.Device.DeviceID, Name: a.Device.Name, PublicKey: a.Device.PublicKey, PairedAt: now})
}

func (n *Node) addPeer(p config.Peer) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Config.UpsertPeer(p)
	return n.saveConfigLocked()
}

// Peer returns a copy of the paired device with the ID.
func (n *Node) Peer(id string) (config.Peer, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := n.Config.PeerByID(id); p != nil {
		cp := *p
		cp.Addresses = slices.Clone(p.Addresses)
		return cp, true
	}
	return config.Peer{}, false
}

// SyncOnce scans both sides and pulls in both directions until neither has
// anything left to apply, or a safety limit is hit.
func SyncOnce(a, b *Node, folderID string) error {
	for round := 0; round < 8; round++ {
		ia, err := a.ExportIndex(folderID)
		if err != nil {
			return err
		}
		ib, err := b.ExportIndex(folderID)
		if err != nil {
			return err
		}
		if !a.differs(ia, ib) && !b.differs(ib, ia) {
			return nil
		}
		if err := a.PullFrom(ib, b.GetBlob); err != nil {
			return fmt.Errorf("a pull: %w", err)
		}
		if err := b.PullFrom(ia, a.GetBlob); err != nil {
			return fmt.Errorf("b pull: %w", err)
		}
	}
	return fmt.Errorf("engine: did not converge")
}

// differs reports whether this node would apply anything to match remote.
func (n *Node) differs(local, remote *reconcile.Index) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	lib := n.Config.LibraryByID(remote.FolderID)
	if lib == nil {
		return false
	}
	return len(n.opsFor(lib, local, n.remoteFor(lib, local, remote))) > 0
}

type Status struct {
	DeviceID  string         `json:"deviceId"`
	Name      string         `json:"name"`
	Listen    string         `json:"listen,omitempty"`
	API       string         `json:"api,omitempty"`
	Libraries []LibraryState `json:"libraries"`
	Peers     []config.Peer  `json:"peers"`
}

type LibraryState struct {
	config.Library
	Files int `json:"files"`
	// Placeholders are files whose content is in iCloud, not on this
	// device, so they don't sync until they're downloaded.
	Placeholders []string `json:"placeholders,omitempty"`
	// ScanErrors are the files the last scan couldn't read, each with why;
	// Skipped are those it doesn't sync, such as symlinks, each with why;
	// and Busy are those that changed as it read them, which the next scan
	// reads again.
	ScanErrors []string `json:"scanErrors,omitempty"`
	Skipped    []string `json:"skipped,omitempty"`
	Busy       []string `json:"busy,omitempty"`
	// HeldDeletes are deletes found here that weren't sent to peers because
	// one scan found too many. ConfirmDeletes sends them; RestoreDeletes
	// puts the files back.
	HeldDeletes []string `json:"heldDeletes,omitempty"`
	// Error is why the last scan failed, if it did.
	Error string `json:"error,omitempty"`
	// Devices are the paired devices the library syncs with.
	Devices []DeviceSync `json:"devices"`
}

// DeviceSync is how a library has synced with a device since this process
// started.
type DeviceSync struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	// LastSync is when a sync last finished.
	LastSync time.Time `json:"lastSync,omitzero"`
	// Failures counts the syncs that failed since. Error is why the last
	// one did, and ErrorAt when; RetryAt is when this device tries again.
	Failures int       `json:"failures,omitempty"`
	Error    string    `json:"error,omitempty"`
	ErrorAt  time.Time `json:"errorAt,omitzero"`
	RetryAt  time.Time `json:"retryAt,omitzero"`
}

func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	listen := n.boundAddr
	if listen == "" {
		listen = n.Config.Listen
	}
	// Callers encode the result after the lock is released, so nothing in it
	// may share memory with the config.
	st := Status{
		DeviceID:  n.Device.DeviceID,
		Name:      n.Device.Name,
		Listen:    listen,
		API:       n.Config.API,
		Libraries: []LibraryState{},
		Peers:     make([]config.Peer, 0, len(n.Config.Peers)),
	}
	for _, p := range n.Config.Peers {
		p.Addresses = slices.Clone(p.Addresses)
		st.Peers = append(st.Peers, p)
	}
	for _, l := range n.Config.Libraries {
		ls := LibraryState{Library: cloneLibrary(l), HeldDeletes: slices.Clone(n.held[l.ID]), Error: n.scanErrs[l.ID], Devices: []DeviceSync{}}
		if res := n.scans[l.ID]; res != nil {
			ls.Placeholders = slices.Clone(res.Placeholders)
			ls.ScanErrors = slices.Clone(res.Errors)
			ls.Skipped = slices.Clone(res.Skipped)
			ls.Busy = slices.Clone(res.Busy)
		}
		for _, p := range n.Config.Peers {
			if !sharesWith(l, p.DeviceID) {
				continue
			}
			m := n.synced[syncPair{p.DeviceID, l.ID}]
			d := DeviceSync{DeviceID: p.DeviceID, Name: p.Name, LastSync: m.at.UTC(), Failures: m.failures, Error: m.err, ErrorAt: m.failedAt.UTC()}
			if m.failures > 0 {
				d.RetryAt = m.failedAt.Add(retryAfter(m.failures)).UTC()
			}
			ls.Devices = append(ls.Devices, d)
		}
		if idx := n.indexes[l.ID]; idx != nil {
			for _, e := range idx.Entries {
				if !e.Deleted && !e.Incomplete {
					ls.Files++
				}
			}
		}
		st.Libraries = append(st.Libraries, ls)
	}
	return st
}
