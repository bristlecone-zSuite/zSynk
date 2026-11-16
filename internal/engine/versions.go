package engine

import (
	"errors"
	"fmt"
	"os"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/ignore"
	"github.com/bristlecone-zSuite/zSynk/internal/packages"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

var (
	// ErrInvalidPath means a path isn't one a library can hold.
	ErrInvalidPath = errors.New("engine: invalid path")
	// ErrDamagedVersion means a version's copy no longer has the content
	// it had when it was kept, so it isn't restored.
	ErrDamagedVersion = errors.New("engine: the version's copy is damaged")
)

// Versions lists the versions the vault keeps of rel in library id, newest
// first. With rel empty, it lists those of every path in the library.
func (n *Node) Versions(id, rel string) ([]vault.Version, error) {
	if _, ok := n.Library(id); !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	if rel == "" {
		return n.Vault.List(id)
	}
	if err := safepath.Check(rel); err != nil {
		return nil, fmt.Errorf("%w %q: %v", ErrInvalidPath, rel, err)
	}
	return n.Vault.Versions(id, rel)
}

// RestoreVersion puts a version the vault keeps of rel back in library id:
// the one named version, or the newest if version is empty. What it
// replaces is kept as a version in turn. Paired devices get the restored
// content at the next sync, as they get any edit made here.
func (n *Node) RestoreVersion(id, rel, version string) (vault.Version, error) {
	if err := safepath.Check(rel); err != nil {
		return vault.Version{}, fmt.Errorf("%w %q: %v", ErrInvalidPath, rel, err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	lib := n.Config.LibraryByID(id)
	if lib == nil {
		return vault.Version{}, fmt.Errorf("%w %q", ErrUnknownLibrary, id)
	}
	if version == "" {
		vs, err := n.Vault.Versions(id, rel)
		if err != nil {
			return vault.Version{}, err
		}
		if len(vs) == 0 {
			return vault.Version{}, fmt.Errorf("%w of %s", vault.ErrNoVersion, rel)
		}
		version = vs[0].ID
	}
	if _, err := n.scanLocked(id); err != nil {
		return vault.Version{}, err
	}
	var ver vault.Version
	var op reconcile.Op
	err := n.Vault.Read(id, rel, version, func(v vault.Version, content string) error {
		ver = v
		var err error
		op, err = n.storeVersion(v, content)
		return err
	})
	if err != nil {
		return vault.Version{}, err
	}
	env, err := apply.Open(lib.Path, lib.ID, n.Store, n.Vault, n.Journal)
	if err != nil {
		return vault.Version{}, err
	}
	defer env.Close()
	// The restore is applied against a copy of the index, so the next scan
	// finds it as an edit made here and sends it on.
	scratch := reconcile.NewIndex(id, n.Device.DeviceID)
	if e, ok := n.indexes[id].Entries[rel]; ok {
		scratch.Entries[rel] = e
		// The vault keeps content only. A restored file stays executable,
		// or not, as it is now or was when it was deleted.
		op.Meta.Exec = e.Exec
	}
	deferred, err := env.Apply([]reconcile.Op{op}, scratch)
	if err != nil {
		return vault.Version{}, err
	}
	if len(deferred) > 0 {
		return vault.Version{}, fmt.Errorf("engine: %s changed as it was restored, so it was left as it is: %w", rel, apply.ErrChangedSinceScan)
	}
	n.Log.Info("restored a version", "library", id, "path", rel, "version", ver.ID)
	if _, err := n.scanLocked(id); err != nil {
		return ver, err
	}
	return ver, nil
}

// storeVersion puts a version's content in the store, and returns the op
// that writes it at the version's path.
func (n *Node) storeVersion(ver vault.Version, content string) (reconcile.Op, error) {
	op := reconcile.Op{Kind: reconcile.OpSet, Path: ver.Path, KindFile: reconcile.Kind(ver.Kind)}
	var err error
	switch op.KindFile {
	case reconcile.KindFile:
		op.Hash, op.Size, err = n.Store.PutFile(content)
	case reconcile.KindPackage:
		err = n.storePackage(content, &op)
	default:
		err = fmt.Errorf("engine: version %s of %s is a %q", ver.ID, ver.Path, ver.Kind)
	}
	if err == nil && op.Hash != ver.Hash {
		err = fmt.Errorf("%w: version %s of %s", ErrDamagedVersion, ver.ID, ver.Path)
	}
	return op, err
}

// storePackage puts the package in dir in the store, as the scanner would,
// and fills in op's content.
func (n *Node) storePackage(dir string, op *reconcile.Op) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	man, raw, err := packages.BuildManifestFS(r.FS(), ignore.Compile(ignore.GlobalPatterns()))
	if err != nil {
		return err
	}
	for _, f := range man.Files {
		op.Size += f.Size
		if n.Store.Has(f.Hash) {
			continue
		}
		part, err := r.Open(f.RelPath)
		if err != nil {
			return err
		}
		h, _, err := n.Store.PutReader(part)
		_ = part.Close()
		if err != nil {
			return err
		}
		if h != f.Hash {
			return fmt.Errorf("%w: %s changed as it was read", ErrDamagedVersion, f.RelPath)
		}
	}
	op.Hash, err = n.Store.Put(raw)
	return err
}
