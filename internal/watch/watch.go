package watch

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
)

// Watcher polls library roots. No extra dependency: macOS FSEvents and
// Linux inotify can replace this later without changing the engine.
type Watcher struct {
	Interval time.Duration
}

func New(interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Watcher{Interval: interval}
}

// Fingerprint is a cheap tree signature: the names in the tree, and for
// each file what the scanner checks to see that it changed. It is not a
// content hash; the engine still scans and SHA-256s before applying.
func Fingerprint(root string) (uint64, error) {
	h := fnv.New64a()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && safepath.IsSynkDir(d.Name()) {
			return fs.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		_, _ = h.Write(append([]byte(rel), 0))
		// A folder's own time and size change with anything in it,
		// including what the scanner skips, such as .synk.
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st := fsmeta.FromInfo(info)
		var buf [40]byte
		binary.LittleEndian.PutUint64(buf[0:], uint64(st.Size))
		binary.LittleEndian.PutUint64(buf[8:], uint64(st.MTime))
		binary.LittleEndian.PutUint64(buf[16:], uint64(st.CTime))
		binary.LittleEndian.PutUint64(buf[24:], st.Ino)
		binary.LittleEndian.PutUint64(buf[32:], uint64(st.Mode))
		_, _ = h.Write(buf[:])
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return h.Sum64(), nil
}

// Run calls onChange(root) when a root's fingerprint changes. The first
// observation is the baseline. rootsFn is called every tick so newly
// enabled libraries are picked up.
func (w *Watcher) Run(ctx context.Context, rootsFn func() []string, onChange func(root string)) {
	seen := map[string]uint64{}
	tick := time.NewTicker(w.Interval)
	defer tick.Stop()
	w.observe(rootsFn, seen, onChange, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.observe(rootsFn, seen, onChange, false)
		}
	}
}

func (w *Watcher) observe(rootsFn func() []string, seen map[string]uint64, onChange func(root string), baseline bool) {
	if rootsFn == nil {
		return
	}
	live := map[string]struct{}{}
	for _, root := range rootsFn() {
		if root == "" {
			continue
		}
		live[root] = struct{}{}
		fp, err := Fingerprint(root)
		if err != nil {
			continue
		}
		prev, ok := seen[root]
		seen[root] = fp
		if baseline || !ok {
			continue
		}
		if prev != fp && onChange != nil {
			onChange(root)
		}
	}
	for root := range seen {
		if _, ok := live[root]; !ok {
			delete(seen, root)
		}
	}
}
