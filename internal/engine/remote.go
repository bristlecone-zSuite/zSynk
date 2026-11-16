package engine

import (
	"fmt"

	"github.com/bristlecone-zSuite/zSynk/internal/blob"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
	"github.com/bristlecone-zSuite/zSynk/internal/vector"
)

// Limits on what a peer's index may contain.
const (
	maxRemoteEntries = 1 << 20
	maxRemoteParts   = 1 << 16
	maxVectorLen     = 256
	maxDeviceIDLen   = 64
	// Counters stay exactly representable as JSON numbers everywhere and far
	// from overflow when this device increments them.
	maxCounter = 1 << 53
)

// checkRemote validates an index that came from a peer before anything uses
// it. Entries that fail are dropped and reported, not fatal: a dropped entry
// reads as "the peer didn't mention this path", which never deletes anything
// here.
func checkRemote(idx *reconcile.Index, folderID string) (*reconcile.Index, []string, error) {
	if idx == nil {
		return nil, nil, fmt.Errorf("engine: peer sent no index")
	}
	if idx.FolderID != folderID {
		return nil, nil, fmt.Errorf("engine: peer sent index for %q, want %q", idx.FolderID, folderID)
	}
	if len(idx.Entries) > maxRemoteEntries {
		return nil, nil, fmt.Errorf("engine: peer index has %d entries, limit %d", len(idx.Entries), maxRemoteEntries)
	}
	out := reconcile.NewIndex(idx.FolderID, idx.DeviceID)
	var dropped []string
	for key, e := range idx.Entries {
		if err := checkRemoteEntry(key, &e); err != nil {
			dropped = append(dropped, fmt.Sprintf("%q: %v", key, err))
			continue
		}
		out.Entries[key] = e
	}
	return out, dropped, nil
}

func checkRemoteEntry(key string, e *reconcile.Entry) error {
	if key != e.Path {
		return fmt.Errorf("key does not match path %q", e.Path)
	}
	if err := safepath.Check(e.Path); err != nil {
		return err
	}
	if e.Kind != reconcile.KindFile && e.Kind != reconcile.KindPackage {
		return fmt.Errorf("unsupported kind %q", e.Kind)
	}
	if err := checkVector(e.Vector); err != nil {
		return err
	}
	if len(e.By) > maxDeviceIDLen {
		return fmt.Errorf("invalid device ID")
	}
	if e.Kind == reconcile.KindPackage {
		e.Exec = false
	}
	if e.Deleted || e.Incomplete {
		// Reconcile never reads content fields from these.
		if blob.ValidateHash(e.Hash) != nil {
			e.Hash = ""
		}
		e.Size = max(e.Size, 0)
		e.Parts = nil
		return nil
	}
	if err := blob.ValidateHash(e.Hash); err != nil {
		return err
	}
	if e.Size < 0 {
		return fmt.Errorf("negative size")
	}
	if e.Kind == reconcile.KindFile {
		e.Parts = nil
		return nil
	}
	if len(e.Parts) > maxRemoteParts {
		return fmt.Errorf("%d parts, limit %d", len(e.Parts), maxRemoteParts)
	}
	for _, p := range e.Parts {
		if err := blob.ValidateHash(p); err != nil {
			return fmt.Errorf("part: %w", err)
		}
	}
	return nil
}

func checkVector(v vector.Vector) error {
	if len(v) > maxVectorLen {
		return fmt.Errorf("version vector has %d devices, limit %d", len(v), maxVectorLen)
	}
	for id, n := range v {
		if id == "" || len(id) > maxDeviceIDLen {
			return fmt.Errorf("version vector has an invalid device ID")
		}
		if n > maxCounter {
			return fmt.Errorf("version vector counter %d is too large", n)
		}
	}
	return nil
}
