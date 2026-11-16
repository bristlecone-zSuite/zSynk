package engine

import (
	"sort"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
)

// foldKey folds a slash-separated path the way a case-folding file system
// does: names that differ only in case share a key. It isn't a full
// Unicode answer — normalization-insensitive volumes can also hold two
// spellings of one name — but it covers the common APFS case, and apply
// catches anything this misses before a file is replaced.
func foldKey(p string) string { return strings.ToLower(p) }

// foldsCase reports whether the volume holding a library folds case. The
// answer is probed once per library and process. Callers may hold n.mu, so
// the cache has its own lock.
func (n *Node) foldsCase(dir string) bool {
	if n.FoldProbe != nil {
		return n.FoldProbe(dir)
	}
	if n.folds == nil {
		return fsmeta.FoldsCase(dir)
	}
	n.foldsMu.Lock()
	defer n.foldsMu.Unlock()
	if folds, ok := n.folds[dir]; ok {
		return folds
	}
	folds := fsmeta.FoldsCase(dir)
	n.folds[dir] = folds
	return folds
}

// remoteFor returns remote as this device should see it. On a volume that
// folds case, paths the index can't hold as named are moved to their
// conflict names, so a peer's two spellings of one name both arrive instead
// of one silently replacing the other.
func (n *Node) remoteFor(lib *config.Library, local, remote *reconcile.Index) *reconcile.Index {
	if remote == nil || !n.foldsCase(lib.Path) {
		return remote
	}
	return foldAdjust(local, remote)
}

// foldAdjust rewrites a copy of remote for a case-folding volume:
//
//   - A remote path that isn't here but folds onto one live local path is
//     renamed to that path, so the two versions are compared as what they
//     are: versions of the same file, which reconcile resolves by vector,
//     with a conflict copy for the loser.
//   - Of the distinct live paths left in a fold group, the one a live local
//     entry holds keeps its name, and otherwise the first in sort order
//     does. A remote entry at another path is dropped when the keeper
//     already holds its content, and otherwise becomes a set at a conflict
//     name spelled like the keeper's path, with the entry's own content
//     hash in the name.
//
// Local entries are never rewritten: their names are what the files on
// disk are. Because conflict names always borrow the keeper's spelling,
// they can't collide with each other, and a second pass finds every copy
// already in place.
func foldAdjust(local, remote *reconcile.Index) *reconcile.Index {
	out := reconcile.NewIndex(remote.FolderID, remote.DeviceID)
	for k, e := range remote.Entries {
		out.Entries[k] = e
	}
	if local == nil {
		local = reconcile.NewIndex("", "")
	}

	localPath := map[string]string{} // fold key -> the one live local path there
	localCount := map[string]int{}
	remoteCount := map[string]int{}
	for p, e := range local.Entries {
		if e.Deleted || e.Incomplete {
			continue
		}
		k := foldKey(p)
		localPath[k] = p
		localCount[k]++
	}
	for p := range remote.Entries {
		remoteCount[foldKey(p)]++
	}

	// Alias: when this volume folds, a remote path that isn't here but
	// names a local file moves onto that file's path, so reconcile compares
	// the versions as versions of one file. Only when the fold group holds
	// exactly one remote entry and one local path; anything richer is left
	// for the rewrite below.
	for p, e := range out.Entries {
		k := foldKey(p)
		if _, here := local.Entries[p]; here || localCount[k] != 1 || remoteCount[k] != 1 {
			continue
		}
		to := localPath[k]
		if _, taken := out.Entries[to]; taken {
			continue
		}
		e.Path = to
		delete(out.Entries, p)
		out.Entries[to] = e
	}

	// Rewrite the losers of each fold group.
	groups := map[string][]string{}
	for p, e := range out.Entries {
		if e.Deleted || e.Incomplete {
			continue
		}
		k := foldKey(p)
		groups[k] = append(groups[k], p)
	}
	localHeld := map[string]bool{}
	for p, e := range local.Entries {
		if e.Deleted || e.Incomplete {
			continue
		}
		localHeld[p] = true
		k := foldKey(p)
		if _, ok := groups[k]; ok {
			groups[k] = append(groups[k], p)
		}
	}
	for _, paths := range groups {
		sort.Strings(paths)
		keeper := paths[0]
		for _, p := range paths {
			if localHeld[p] {
				keeper = p
				break
			}
		}
		want, have := local.Entries[keeper]
		if !have {
			want, have = out.Entries[keeper]
		}
		for _, p := range paths {
			if p == keeper {
				continue
			}
			e, ok := out.Entries[p]
			if !ok || e.Deleted || e.Incomplete {
				continue
			}
			// Content the keeper already holds needs no second file here.
			if have && want.Hash == e.Hash && want.Kind == e.Kind {
				delete(out.Entries, p)
				continue
			}
			to := reconcile.ConflictPath(keeper, e.Hash)
			if there, taken := out.Entries[to]; taken && !there.Deleted && !there.Incomplete {
				if there.Hash == e.Hash && there.Kind == e.Kind {
					delete(out.Entries, p)
				}
				continue
			}
			e.Path = to
			delete(out.Entries, p)
			out.Entries[to] = e
		}
	}
	return out
}
