package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/fsmeta"
	"github.com/bristlecone-zSuite/zSynk/internal/scan"
)

// Idle daemons don't read files they've read before, and still sync a
// change as soon as it's made.
func TestIdleDaemonsReadNothing(t *testing.T) {
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("needs /proc/self/io to count the bytes read")
	}
	tick(t, 50*time.Millisecond)
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	listen(t, b)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	a.RacyWindow, b.RacyWindow = 0, 0
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	const size = 8 << 20
	writeFile(t, dirA, "talk.mov", strings.Repeat("v", size))
	must(t, b.SyncAllPeers())
	_, err := b.ScanLibrary("notes")
	must(t, err)

	before := bytesRead(t)
	stopA, stopB := run(t, a), run(t, b)
	time.Sleep(time.Second)
	writeFile(t, dirA, "todo.md", "while idle")
	waitForFile(t, dirB, "todo.md", "a change made while the daemons were idle didn't sync")
	stopA()
	stopB()
	if read := bytesRead(t) - before; read > size/4 {
		t.Fatalf("two idle daemons read %d KiB for one unchanged %d MiB file", read>>10, size>>20)
	}
}

// A library that changed on neither device waits for resync before it
// syncs again. A change syncs at once, and the device that got it confirms
// the match once.
func TestUnchangedLibrariesWaitToResync(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	listen(t, b)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	seen := func() time.Time {
		p, _ := a.Peer(b.Device.DeviceID)
		return p.LastSeen
	}
	must(t, b.SyncChanged())
	last := seen()
	if last.IsZero() {
		t.Fatal("B didn't sync a library it had never synced")
	}
	must(t, b.SyncChanged())
	must(t, a.SyncChanged())
	if !seen().Equal(last) {
		t.Fatal("a library that changed on neither device synced again")
	}

	writeFile(t, dirB, "todo.md", "new")
	_, err := b.ScanLibrary("notes")
	must(t, err)
	must(t, b.SyncChanged())
	wantFile(t, dirA, "todo.md", "new")
	last = seen()
	must(t, a.SyncChanged())
	if !seen().After(last) {
		t.Fatal("A didn't confirm the change it got")
	}
	last = seen()
	must(t, a.SyncChanged())
	must(t, b.SyncChanged())
	if !seen().Equal(last) {
		t.Fatal("the devices kept syncing a library they agree on")
	}

	locked(b, func() {
		for k, m := range b.synced {
			m.at = m.at.Add(-resync)
			b.synced[k] = m
		}
	})
	must(t, b.SyncChanged())
	if !seen().After(last) {
		t.Fatal("B didn't sync again after resync passed")
	}
}

// The loop syncs a change made on this device without waiting for its tick.
func TestLoopSyncsChangesAtOnce(t *testing.T) {
	tick(t, time.Hour)
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	listen(t, b)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	run(t, a)
	// The watcher reports changes from its first look at the folder.
	time.Sleep(500 * time.Millisecond)
	writeFile(t, dirA, "todo.md", "now")
	waitForFile(t, dirB, "todo.md", "a change waited for the loop's tick")
}

// The loop's tick syncs libraries that changed only on the other device,
// once resync passes.
func TestLoopResyncs(t *testing.T) {
	tick(t, 50*time.Millisecond)
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	listen(t, b)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	dirA, dirB := library(t, a, "notes"), library(t, b, "notes")
	must(t, a.SyncChanged())
	run(t, a)
	writeFile(t, dirB, "todo.md", "from the iPhone")
	locked(a, func() {
		for k, m := range a.synced {
			m.at = m.at.Add(-resync)
			a.synced[k] = m
		}
	})
	waitForFile(t, dirA, "todo.md", "the loop never resynced")
}

// The loop doesn't read paused libraries.
func TestLoopSkipsPausedLibraries(t *testing.T) {
	tick(t, 20*time.Millisecond)
	n := openNode(t, "Mac")
	dir := library(t, n, "notes")
	pause(t, n, "notes", true)
	stop := run(t, n)
	writeFile(t, dir, "new.md", "while paused")
	time.Sleep(15 * loopTick)
	stop()
	locked(n, func() {
		if idx := n.indexes["notes"]; idx != nil {
			if _, ok := idx.Entries["new.md"]; ok {
				t.Error("the loop scanned a paused library")
			}
		}
	})
}

// tick sets loopTick until the test ends. Call it before run, so the loop
// stops before loopTick is reset.
func tick(t *testing.T, d time.Duration) {
	old := loopTick
	loopTick = d
	t.Cleanup(func() { loopTick = old })
}

// run runs n's loop until the test ends, or until the returned stop is
// called.
func run(t *testing.T, n *Node) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.WatchAndSync(ctx)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}

func waitForFile(t *testing.T, dir, name, failure string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
	}
}

// A scan that finds nothing new writes nothing, and what scans learned
// outlasts a restart.
func TestScansWriteOnlyChanges(t *testing.T) {
	n := openNode(t, "Mac")
	n.RacyWindow = 0
	dir := library(t, n, "notes")
	writeFile(t, dir, "a.md", "hello")
	scanned := func() {
		t.Helper()
		_, err := n.ScanLibrary("notes")
		must(t, err)
	}
	written := func() []fsmeta.Stat {
		t.Helper()
		var out []fsmeta.Stat
		for _, p := range []string{n.indexPath("notes"), n.cachePath("notes")} {
			fi, err := os.Stat(p)
			must(t, err)
			out = append(out, fsmeta.FromInfo(fi))
		}
		return out
	}
	scanned()
	before := written()
	scanned()
	if !slices.Equal(written(), before) {
		t.Fatal("a scan that found no change rewrote the index or the cache")
	}
	writeFile(t, dir, "b.md", "new")
	scanned()
	if after := written(); after[0] == before[0] || after[1] == before[1] {
		t.Fatal("a scan that found a new file didn't save the index and the cache")
	}

	must(t, n.Close())
	n = openNodeAt(t, n.DataDir, "Mac")
	var cache *scan.Cache
	locked(n, func() { cache = n.scanCache("notes") })
	if len(cache.Entries) != 2 {
		t.Fatalf("after a restart, the cache has %v", cache.Entries)
	}
}

func bytesRead(t *testing.T) int64 {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/io")
	must(t, err)
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "rchar: "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			must(t, err)
			return n
		}
	}
	t.Fatal("no rchar in /proc/self/io")
	return 0
}
