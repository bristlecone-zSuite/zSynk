package engine

import (
	"context"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/watch"
)

// loopTick is how often WatchAndSync scans every library and syncs those
// that are due.
var loopTick = 5 * time.Second

// tidyEvery is how often WatchAndSync removes what the store no longer
// needs, and the versions the vault no longer keeps.
const tidyEvery = time.Hour

// WatchAndSync scans libraries when their trees change, and periodically,
// and syncs them with paired devices when they change or resync passes.
// It returns once ctx is done and nothing it started is still running.
func (n *Node) WatchAndSync(ctx context.Context) {
	w := watch.New(2 * time.Second)
	watching := make(chan struct{})
	defer func() { <-watching }()
	go n.watch(ctx, w, watching)

	t := time.NewTicker(loopTick)
	defer t.Stop()
	var tidied time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.mu.Lock()
			libs := append([]config.Library(nil), n.Config.Libraries...)
			n.mu.Unlock()
			for _, l := range libs {
				if l.Paused {
					continue
				}
				_, _ = n.ScanLibrary(l.ID)
			}
			_ = n.SyncChanged()
			if time.Since(tidied) >= tidyEvery {
				n.tidy()
				tidied = time.Now()
			}
		}
	}
}

// tidy removes what the store no longer needs, and the versions the vault
// no longer keeps.
func (n *Node) tidy() {
	removed, freed, err := n.CollectGarbage()
	switch {
	case err != nil:
		n.Log.Warn("couldn't remove unused content", "err", err)
	case removed > 0:
		n.Log.Info("removed unused content", "files", removed, "bytes", freed)
	}
	removed, freed, err = n.Vault.Prune(time.Now())
	switch {
	case err != nil:
		n.Log.Warn("couldn't remove old versions", "err", err)
	case removed > 0:
		n.Log.Info("removed old versions", "versions", removed, "bytes", freed)
	}
}

// watch scans a library when its tree changes, and syncs it, until ctx is
// done. It closes done when it returns.
func (n *Node) watch(ctx context.Context, w *watch.Watcher, done chan<- struct{}) {
	defer close(done)
	w.Run(ctx, func() []string {
		n.mu.Lock()
		defer n.mu.Unlock()
		var roots []string
		for _, l := range n.Config.Libraries {
			if l.Paused || l.Path == "" {
				continue
			}
			roots = append(roots, l.Path)
		}
		return roots
	}, func(root string) {
		n.mu.Lock()
		var id string
		for _, l := range n.Config.Libraries {
			if l.Path == root {
				id = l.ID
				break
			}
		}
		n.mu.Unlock()
		if id == "" {
			return
		}
		_, _ = n.ScanLibrary(id)
		_ = n.SyncChanged()
	})
}
