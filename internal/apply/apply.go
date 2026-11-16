// Package apply changes files in a library to match reconciled ops.
//
// Every file operation goes through an os.Root on the library, so no path
// can leave it. Changes are staged in <library>/.synk/staging, on the same
// volume as their destination, and journaled so a crash is either finished
// or rolled back. Nothing is replaced or deleted unless it still matches
// the index and a vault snapshot of it succeeded.
package apply

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/ignore"
	"github.com/bristlecone-zSuite/zSynk/internal/journal"
	"github.com/bristlecone-zSuite/zSynk/internal/packages"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

// MinFree is left free on each volume Synk writes to.
const MinFree = 64 * 1024 * 1024

const stagingDir = safepath.SynkDir + "/staging"

var (
	// ErrChangedSinceScan means the destination no longer matches the index.
	// Applying the op could destroy an edit no device has seen, so it's
	// skipped. The next scan records the edit and the next sync resolves it,
	// with a conflict copy if needed.
	ErrChangedSinceScan = errors.New("apply: destination changed since the last scan")
	ErrNoMarker         = errors.New("apply: library has no .synk directory")
	// ErrNotADir means a folder on the way to a path is a symlink or a file
	// here but a folder on the peer. Synk never follows links: writing
	// through one puts content where the scanner doesn't look.
	ErrNotADir = errors.New("apply: a folder on the path is a symlink or a file on this device")
	errJournal = errors.New("apply: journal write failed")
)

type Env struct {
	Root     string
	FolderID string
	Store    *blob.Store
	Vault    *vault.Vault
	Journal  *journal.Log
	root     *os.Root
	ig       *ignore.Matcher
	// execBits is whether the library's file system keeps executable bits.
	execBits bool
}

// Open prepares to apply changes to the library at libRoot. The library
// must already contain its .synk directory; Open never creates it, because
// an empty mount point that gained a marker would look like a library whose
// files were all deleted.
func Open(libRoot, folderID string, store *blob.Store, v *vault.Vault, j *journal.Log) (*Env, error) {
	r, err := os.OpenRoot(libRoot)
	if err != nil {
		return nil, err
	}
	if st, err := r.Lstat(safepath.SynkDir); err != nil || !st.IsDir() {
		_ = r.Close()
		return nil, fmt.Errorf("%w: %s", ErrNoMarker, libRoot)
	}
	if err := r.MkdirAll(stagingDir, 0o700); err != nil {
		_ = r.Close()
		return nil, err
	}
	return &Env{
		Root: libRoot, FolderID: folderID, Store: store, Vault: v, Journal: j,
		root: r, ig: ignore.Compile(ignore.GlobalPatterns()), execBits: fsmeta.ExecBits(libRoot),
	}, nil
}

func (e *Env) Close() error { return e.root.Close() }

// Recover finishes or rolls back this library's transactions that a crash
// interrupted, then removes staging nothing owns. Run it before the library
// is scanned: a half-swapped package would otherwise scan as deleted.
func (e *Env) Recover() error {
	for _, r := range e.Journal.Incomplete(e.FolderID) {
		if err := e.recoverOne(r); err != nil {
			return err
		}
	}
	ents, err := fs.ReadDir(e.root.FS(), stagingDir)
	if err != nil {
		return err
	}
	for _, d := range ents {
		if e.Journal.Finished(d.Name()) {
			if err := e.root.RemoveAll(path.Join(stagingDir, d.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Env) recoverOne(r journal.Record) error {
	stage := path.Join(stagingDir, r.ID)
	if !validTx(r.ID) || r.Staging != stage || safepath.Check(r.Path) != nil {
		// Older versions staged outside the library. Leave what they left;
		// the vault has a snapshot of anything they had started to replace.
		return e.log(r.ID, journal.StatusAborted, r.Op, r.Path, "", "not recovered: unrecognized staging")
	}
	if r.Status == journal.StatusPrepared && r.Op == "set" && !e.exists(path.Join(stage, "new")) {
		// The new content was moved into place before the crash.
		if err := e.root.RemoveAll(stage); err != nil {
			return err
		}
		return e.log(r.ID, journal.StatusCommitted, r.Op, r.Path, "", "finished after a crash")
	}
	if err := e.restore(r.Path, stage); err != nil {
		return err
	}
	if err := e.root.RemoveAll(stage); err != nil {
		return err
	}
	return e.log(r.ID, journal.StatusAborted, r.Op, r.Path, "", "rolled back after a crash")
}

// restore moves a destination that was set aside back into place, unless
// something new has appeared there since.
func (e *Env) restore(rel, stage string) error {
	old := path.Join(stage, "old")
	if !e.exists(old) || e.exists(rel) {
		return nil
	}
	return e.root.Rename(old, rel)
}

// Apply makes the library match ops and records each applied op in idx. Ops
// whose destination changed since the scan are skipped and returned in
// deferred. Other per-op failures don't stop the batch; they're joined into
// err. A journal failure stops it.
func (e *Env) Apply(ops []reconcile.Op, idx *reconcile.Index) (deferred []string, err error) {
	if err := e.checkSpace(ops, idx); err != nil {
		return nil, err
	}
	var errs []error
	for _, op := range ordered(ops) {
		err := e.applyOne(op, idx)
		switch {
		case err == nil:
		case errors.Is(err, ErrChangedSinceScan):
			deferred = append(deferred, op.Path)
		case errors.Is(err, errJournal):
			return deferred, errors.Join(append(errs, err)...)
		default:
			errs = append(errs, fmt.Errorf("%s: %w", op.Path, err))
		}
	}
	return deferred, errors.Join(errs...)
}

// ordered puts new content first and deletes after it, so a batch that
// stops partway never leaves a rename with the file only in the vault.
func ordered(ops []reconcile.Op) []reconcile.Op {
	phase := func(k reconcile.OpKind) int {
		switch k {
		case reconcile.OpSet, reconcile.OpConflict:
			return 0
		case reconcile.OpDelete:
			return 1
		default:
			return 2
		}
	}
	out := append([]reconcile.Op(nil), ops...)
	sort.SliceStable(out, func(i, j int) bool { return phase(out[i].Kind) < phase(out[j].Kind) })
	return out
}

func (e *Env) checkSpace(ops []reconcile.Op, idx *reconcile.Index) error {
	var incoming, snapshots uint64
	for _, op := range ops {
		if op.Kind == reconcile.OpSet || op.Kind == reconcile.OpConflict {
			incoming += uint64(op.Size) + uint64(op.ConflictSize)
		}
		if op.Kind != reconcile.OpMergeVector {
			if cur, ok := idx.Entries[op.Path]; ok && !cur.Deleted {
				snapshots += uint64(cur.Size)
			}
		}
	}
	free, err := atomicfile.DiskAvailable(e.Root)
	if err != nil {
		return nil
	}
	if atomicfile.SameVolume(e.Root, e.Vault.Root) {
		if need := incoming + snapshots + MinFree; free < need {
			return fmt.Errorf("apply: not enough free space (have %d, need %d for new content and snapshots, plus %d reserve)", free, incoming+snapshots, MinFree)
		}
		return nil
	}
	if free < incoming+MinFree {
		return fmt.Errorf("apply: not enough free space in the library (have %d, need %d plus %d reserve)", free, incoming, MinFree)
	}
	if vfree, err := atomicfile.DiskAvailable(e.Vault.Root); err == nil && vfree < snapshots+MinFree {
		return fmt.Errorf("apply: not enough free space for vault snapshots (have %d, need %d plus %d reserve)", vfree, snapshots, MinFree)
	}
	return nil
}

func (e *Env) applyOne(op reconcile.Op, idx *reconcile.Index) error {
	if err := safepath.Check(op.Path); err != nil {
		return fmt.Errorf("apply: %q: %w", op.Path, err)
	}
	switch op.Kind {
	case reconcile.OpMergeVector:
		if ent, ok := idx.Entries[op.Path]; ok {
			ent.Vector = op.Vector
			idx.Entries[op.Path] = ent
		}
		return nil
	case reconcile.OpDelete:
		return e.applyDelete(op, idx)
	case reconcile.OpSet:
		return e.applySet(op.Path, target{op.KindFile, op.Hash, op.Size, op.Meta}, op, idx)
	case reconcile.OpConflict:
		if err := safepath.Check(op.ConflictPath); err != nil {
			return fmt.Errorf("apply: %q: %w", op.ConflictPath, err)
		}
		kind := op.ConflictKind
		if kind == "" {
			kind = op.KindFile
		}
		// The losing version gets its conflict copy before the winner can
		// replace it at the original path.
		lose := reconcile.Op{Kind: reconcile.OpSet, Path: op.ConflictPath, KindFile: kind, Hash: op.ConflictHash, Size: op.ConflictSize, Vector: op.Vector}
		if err := e.applySet(lose.Path, target{kind, op.ConflictHash, op.ConflictSize, op.ConflictMeta}, lose, idx); err != nil {
			return err
		}
		return e.applySet(op.Path, target{op.KindFile, op.Hash, op.Size, op.Meta}, op, idx)
	default:
		return fmt.Errorf("apply: unknown op %s", op.Kind)
	}
}

type target struct {
	kind reconcile.Kind
	hash string
	size int64
	meta reconcile.Meta
}

func (e *Env) applySet(rel string, want target, op reconcile.Op, idx *reconcile.Index) error {
	if err := e.checkParents(rel); err != nil {
		return err
	}
	cur, live := liveEntry(idx, rel)
	if live && cur.Hash == want.hash && cur.Kind == want.kind {
		// Already in place, as on the winning side of a conflict, or when
		// only the executable bit changed.
		st, exists, err := e.lstat(rel)
		if err != nil {
			return err
		}
		if !exists {
			return ErrChangedSinceScan
		}
		if ok, err := e.unchanged(rel, st, cur); err != nil || !ok {
			return errOr(err, ErrChangedSinceScan)
		}
		if err := e.setExec(rel, st, want); err != nil {
			return err
		}
		e.record(idx, rel, want, cur.Parts, op)
		return nil
	}

	tx := newTx()
	stage := path.Join(stagingDir, tx)
	if err := e.log(tx, journal.StatusStarted, "set", rel, want.hash, ""); err != nil {
		return err
	}
	if err := e.root.Mkdir(stage, 0o700); err != nil {
		return e.rollback(tx, "set", rel, stage, err)
	}
	parts, err := e.stage(path.Join(stage, "new"), want)
	if err != nil {
		return e.rollback(tx, "set", rel, stage, err)
	}

	st, exists, err := e.lstat(rel)
	if err != nil {
		return e.rollback(tx, "set", rel, stage, err)
	}
	if exists != live {
		// Created or deleted since the scan.
		return e.rollback(tx, "set", rel, stage, ErrChangedSinceScan)
	}
	if exists {
		if err := e.snapshotUnchanged(rel, st, cur, "pre-overwrite"); err != nil {
			return e.rollback(tx, "set", rel, stage, err)
		}
	}
	if err := e.log(tx, journal.StatusPrepared, "set", rel, want.hash, ""); err != nil {
		return err
	}
	if err := e.swap(rel, stage, exists && (st.IsDir() || want.kind == reconcile.KindPackage)); err != nil {
		return e.rollback(tx, "set", rel, stage, err)
	}
	if want.kind == reconcile.KindPackage {
		// Moving a folder to another parent can change its time, so the
		// package gets its time once it's in place.
		e.setTime(rel, want.meta)
	}
	if err := e.log(tx, journal.StatusCommitted, "set", rel, want.hash, ""); err != nil {
		return err
	}
	_ = e.root.RemoveAll(stage)
	e.record(idx, rel, want, parts, op)
	return nil
}

func (e *Env) applyDelete(op reconcile.Op, idx *reconcile.Index) error {
	rel := op.Path
	if err := e.checkParents(rel); err != nil {
		return err
	}
	cur, live := liveEntry(idx, rel)
	st, exists, err := e.lstat(rel)
	if err != nil {
		return err
	}
	if !exists {
		e.tombstone(idx, op, cur)
		return nil
	}
	if !live {
		return ErrChangedSinceScan
	}
	tx := newTx()
	stage := path.Join(stagingDir, tx)
	if err := e.log(tx, journal.StatusStarted, "delete", rel, cur.Hash, ""); err != nil {
		return err
	}
	if err := e.root.Mkdir(stage, 0o700); err != nil {
		return e.rollback(tx, "delete", rel, stage, err)
	}
	if err := e.snapshotUnchanged(rel, st, cur, "pre-delete"); err != nil {
		return e.rollback(tx, "delete", rel, stage, err)
	}
	if err := e.log(tx, journal.StatusPrepared, "delete", rel, cur.Hash, ""); err != nil {
		return err
	}
	// A rename is atomic even for a package; removing in place isn't.
	if err := e.root.Rename(rel, path.Join(stage, "old")); err != nil {
		return e.rollback(tx, "delete", rel, stage, err)
	}
	_ = packages.SyncDir(e.root, path.Dir(rel))
	if err := e.log(tx, journal.StatusCommitted, "delete", rel, cur.Hash, ""); err != nil {
		return err
	}
	_ = e.root.RemoveAll(stage)
	e.removeEmptyParents(rel)
	e.tombstone(idx, op, cur)
	return nil
}

// snapshotUnchanged checks that rel still holds what the index recorded,
// keeps a vault snapshot of it, and checks that nothing changed while the
// snapshot was taken. Any failure means rel must not be replaced or deleted.
func (e *Env) snapshotUnchanged(rel string, st fs.FileInfo, cur reconcile.Entry, reason string) error {
	before, err := e.signature(rel)
	if err != nil {
		return err
	}
	if ok, err := e.unchanged(rel, st, cur); err != nil || !ok {
		return errOr(err, ErrChangedSinceScan)
	}
	if err := e.snapshot(rel, st, cur.Hash, reason); err != nil {
		return fmt.Errorf("apply: leaving %s in place because its vault snapshot failed: %w", rel, err)
	}
	after, err := e.signature(rel)
	if err != nil {
		return err
	}
	if after != before {
		return ErrChangedSinceScan
	}
	return nil
}

// unchanged reports whether rel still has the content the index recorded.
func (e *Env) unchanged(rel string, st fs.FileInfo, cur reconcile.Entry) (bool, error) {
	switch {
	case cur.Kind == reconcile.KindFile && st.Mode().IsRegular():
		if st.Size() != cur.Size {
			return false, nil
		}
		f, err := e.root.Open(rel)
		if err != nil {
			return false, err
		}
		defer f.Close()
		h, _, err := atomicfile.HashReader(f)
		return h == cur.Hash, err
	case cur.Kind == reconcile.KindPackage && st.IsDir():
		sub, err := e.root.OpenRoot(rel)
		if err != nil {
			return false, err
		}
		defer sub.Close()
		_, raw, err := packages.BuildManifestFS(sub.FS(), e.ig)
		if err != nil {
			return false, err
		}
		return packages.HashManifest(raw) == cur.Hash, nil
	default:
		return false, nil
	}
}

// signature summarizes the attributes of rel, and of everything inside it
// if it's a directory, that change when anything is written.
func (e *Env) signature(rel string) (string, error) {
	st, err := e.root.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return fmt.Sprintf("%+v", fsmeta.FromInfo(st)), nil
	}
	var b strings.Builder
	err = fs.WalkDir(e.root.FS(), rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s\x00%+v\n", p, fsmeta.FromInfo(fi))
		return nil
	})
	return b.String(), err
}

func (e *Env) snapshot(rel string, st fs.FileInfo, hash, reason string) error {
	if st.IsDir() {
		sub, err := e.root.OpenRoot(rel)
		if err != nil {
			return err
		}
		defer sub.Close()
		_, err = e.Vault.SnapshotTree(e.FolderID, rel, sub.FS(), hash, reason)
		return err
	}
	f, err := e.root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = e.Vault.SnapshotFile(e.FolderID, rel, f, hash, reason)
	return err
}

// stage writes the target content at dst inside staging. For a package it
// returns the part hashes from the manifest.
func (e *Env) stage(dst string, want target) ([]string, error) {
	if want.kind == reconcile.KindPackage {
		return e.stagePackage(dst, want)
	}
	src, n, err := e.Store.Open(want.hash)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	if n != want.size {
		return nil, fmt.Errorf("apply: content %s is %d bytes, the index says %d", want.hash, n, want.size)
	}
	perm := os.FileMode(0o600)
	if want.meta.Exec {
		perm = 0o700
	}
	out, err := e.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(out, src)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		e.setTime(dst, want.meta)
	}
	return nil, err
}

func (e *Env) stagePackage(dst string, want target) ([]string, error) {
	raw, err := e.Store.Get(want.hash)
	if err != nil {
		return nil, fmt.Errorf("apply: missing package manifest %s: %w", want.hash, err)
	}
	man, err := packages.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	var total int64
	parts := make([]string, 0, len(man.Files))
	for _, f := range man.Files {
		total += f.Size
		parts = append(parts, f.Hash)
	}
	if total != want.size {
		return nil, fmt.Errorf("apply: package %s holds %d bytes, the index says %d", want.hash, total, want.size)
	}
	if err := e.root.Mkdir(dst, 0o700); err != nil {
		return nil, err
	}
	sub, err := e.root.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	if err := packages.Materialize(sub, man, e.Store.Open); err != nil {
		if errors.Is(err, blob.ErrCorrupt) || errors.Is(err, packages.ErrManifestMismatch) {
			_, _ = e.Vault.Quarantine(want.hash, raw, err.Error())
		}
		return nil, err
	}
	return parts, nil
}

// swap moves staged content to rel. A file replacing a file is one atomic
// rename. Anything involving a directory sets the old one aside first, and
// recovery puts it back if a crash lands between the two renames.
func (e *Env) swap(rel, stage string, viaOld bool) error {
	if d := path.Dir(rel); d != "." {
		if err := e.root.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	newPath := path.Join(stage, "new")
	if viaOld {
		if err := e.root.Rename(rel, path.Join(stage, "old")); err != nil {
			return err
		}
	}
	if err := e.root.Rename(newPath, rel); err != nil {
		return err
	}
	// The swap has happened; failing now would roll back a finished rename.
	_ = packages.SyncDir(e.root, path.Dir(rel))
	return nil
}

// rollback undoes a transaction that hasn't committed. If a destination that
// was set aside can't be put back, the transaction stays open and the next
// Recover retries.
func (e *Env) rollback(tx, op, rel, stage string, cause error) error {
	if err := e.restore(rel, stage); err != nil {
		return fmt.Errorf("%w (restoring %s also failed: %v)", cause, rel, err)
	}
	_ = e.root.RemoveAll(stage)
	if err := e.log(tx, journal.StatusAborted, op, rel, "", cause.Error()); err != nil {
		return err
	}
	return cause
}

// removeEmptyParents removes directories left empty by a delete, up to but
// not including the library root.
func (e *Env) removeEmptyParents(rel string) {
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		if e.root.Remove(d) != nil {
			return
		}
	}
}

// setExec gives rel, a file that already has the target's content, the
// target's executable bit. As chmod +x does, it makes the file executable
// for whoever can read it.
func (e *Env) setExec(rel string, st fs.FileInfo, want target) error {
	if want.kind != reconcile.KindFile || !e.execBits || fsmeta.Exec(st.Mode()) == want.meta.Exec {
		return nil
	}
	mode := st.Mode().Perm() &^ 0o111
	if want.meta.Exec {
		mode |= (mode & 0o444) >> 2
	}
	return e.root.Chmod(rel, mode)
}

// setTime gives rel the target's modification time, if it has one. A file
// system that can't keep it still gets the content.
func (e *Env) setTime(rel string, m reconcile.Meta) {
	if m.MTime != 0 {
		_ = e.root.Chtimes(rel, time.Time{}, time.Unix(0, m.MTime))
	}
}

func (e *Env) record(idx *reconcile.Index, rel string, want target, parts []string, op reconcile.Op) {
	idx.Entries[rel] = reconcile.Entry{Path: rel, Kind: want.kind, Hash: want.hash, Size: want.size, Vector: op.Vector, Parts: parts, Meta: want.meta}
}

func (e *Env) tombstone(idx *reconcile.Index, op reconcile.Op, cur reconcile.Entry) {
	kind := cur.Kind
	if kind == "" {
		kind = op.KindFile
	}
	idx.Entries[op.Path] = reconcile.Entry{Path: op.Path, Kind: kind, Hash: cur.Hash, Size: cur.Size, Deleted: true, Vector: op.Vector, Meta: op.Meta}
}

// log journals a transaction step. Open steps record their staging
// directory, which is how recovery finds what to finish or undo.
func (e *Env) log(tx string, st journal.Status, op, rel, hash, note string) error {
	r := journal.Record{ID: tx, Status: st, FolderID: e.FolderID, Op: op, Path: rel, Hash: hash, Note: note}
	if st == journal.StatusStarted || st == journal.StatusPrepared {
		r.Staging = path.Join(stagingDir, tx)
	}
	if _, err := e.Journal.Append(r); err != nil {
		return fmt.Errorf("%w: %v", errJournal, err)
	}
	return nil
}

// checkParents fails unless every existing folder on the way to rel is a
// real directory.
func (e *Env) checkParents(rel string) error {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		d := strings.Join(parts[:i], "/")
		st, exists, err := e.lstat(d)
		if err != nil || !exists {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("%w: %s", ErrNotADir, d)
		}
	}
	return nil
}

func (e *Env) lstat(rel string) (fs.FileInfo, bool, error) {
	st, err := e.root.Lstat(rel)
	if err == nil {
		return st, true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

func (e *Env) exists(rel string) bool {
	_, ok, _ := e.lstat(rel)
	return ok
}

func liveEntry(idx *reconcile.Index, rel string) (reconcile.Entry, bool) {
	cur, ok := idx.Entries[rel]
	return cur, ok && !cur.Deleted && !cur.Incomplete
}

func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func newTx() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "tx-" + hex.EncodeToString(b[:])
}

func validTx(id string) bool {
	h, ok := strings.CutPrefix(id, "tx-")
	if !ok || len(h) != 16 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && strings.ToLower(h) == h
}
