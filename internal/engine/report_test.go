package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/reconcile"
	"github.com/bristlecone-zSuite/zSynk/internal/transport"
)

// Status shows when each library last synced with each of its devices, and
// why the syncs since then failed. Both ends of a sync count it.
func TestStatusShowsHowSyncsWent(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	addrA := listen(t, a)
	joinOffer(t, b, addrA, offer(t, a), a.Fingerprint())
	library(t, b, "notes")
	if d := deviceSync(t, b, "notes", a); d != (DeviceSync{DeviceID: a.Device.DeviceID, Name: "Mac"}) {
		t.Fatalf("before any sync: %+v", d)
	}

	// A has no notes library, so it refuses.
	if err := b.SyncAllPeers(); !errors.Is(err, ErrRefused) {
		t.Fatalf("B synced a library A doesn't have: %v", err)
	}
	failed := deviceSync(t, b, "notes", a)
	if failed.Failures != 1 || !strings.Contains(failed.Error, "not shared") || failed.ErrorAt.IsZero() ||
		!failed.RetryAt.Equal(failed.ErrorAt.Add(retryMin)) || !failed.LastSync.IsZero() {
		t.Fatalf("after a refused sync: %+v", failed)
	}

	library(t, a, "notes")
	must(t, b.SyncAllPeers())
	if d := deviceSync(t, b, "notes", a); d.LastSync.IsZero() || d.Failures != 0 || d.Error != "" || !d.ErrorAt.IsZero() || !d.RetryAt.IsZero() {
		t.Fatalf("after a sync that worked: %+v", d)
	}
	for deadline := time.Now().Add(5 * time.Second); deviceSync(t, a, "notes", b).LastSync.IsZero(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("A didn't count a sync B started")
		}
	}
}

// The device that answers a sync shows it in Activity while it runs, and
// counts it as failed when the other device sends an index it can't use.
func TestAnsweringASyncShowsAndCountsIt(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	library(t, a, "notes")
	start := time.Now()
	c, err := transport.Dial(addrA, time.Second, transport.Config(b.cert, b.anyPeer))
	must(t, err)
	defer c.Close()
	must(t, c.SetDeadline(time.Now().Add(5*time.Second)))
	must(t, transport.WriteMsg(c, transport.Msg{Kind: transport.KindHello, FolderID: "notes"}))
	if m, err := transport.ReadMsg(c); err != nil || m.Kind != transport.KindHelloOK {
		t.Fatalf("%+v, %v; want %s", m, err, transport.KindHelloOK)
	}
	var theirs reconcile.Index
	must(t, readBody(c, transport.KindIndex, &theirs))
	syncing := Task{Library: "notes", Doing: Syncing, DeviceID: b.Device.DeviceID}
	if got := a.Activity(); len(got) != 1 || !hasTask(got, syncing) || got[0].Since.Before(start) {
		t.Fatalf("Activity shows %+v while A answers a sync that started at %v", got, start)
	}

	idx, err := encodeBody(transport.KindIndex, reconcile.NewIndex("other", b.Device.DeviceID))
	must(t, err)
	must(t, transport.WriteBody(c, transport.Msg{Kind: transport.KindIndex, FolderID: "notes"}, idx))
	if m, err := transport.ReadMsg(c); err != nil || m.Kind != transport.KindError {
		t.Fatalf("A answered an index for another library with %+v, %v", m, err)
	}
	for deadline := time.Now().Add(5 * time.Second); deviceSync(t, a, "notes", b).Failures == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("A didn't count the sync as failed: %+v", deviceSync(t, a, "notes", b))
		}
	}
	if d := deviceSync(t, a, "notes", b); d.Failures != 1 || !strings.Contains(d.Error, `index for "other"`) || !d.LastSync.IsZero() {
		t.Fatalf("after B sent an index for another library: %+v", d)
	}
	if got := a.Activity(); len(got) != 0 {
		t.Fatalf("Activity shows %+v after the sync ended", got)
	}
}

// A sync fails on the device that answers it when that device can't scan
// the library, and it counts there too.
func TestAnsweringDeviceCountsItsOwnFailures(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	addrA := listen(t, a)
	must(t, os.RemoveAll(library(t, a, "notes")))
	library(t, b, "notes")
	if err := b.SyncWith(addrA, "notes"); err == nil {
		t.Fatal("synced with a device whose library is missing")
	}
	for deadline := time.Now().Add(5 * time.Second); deviceSync(t, a, "notes", b).Failures == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("A didn't count the sync as failed: %+v", deviceSync(t, a, "notes", b))
		}
	}
	if d := deviceSync(t, a, "notes", b); d.Failures != 1 || d.Error == "" || !d.LastSync.IsZero() {
		t.Fatalf("after A couldn't scan: %+v", d)
	}
}

// Status shows the files a library's last scan couldn't read, didn't
// sync, or found changing. The log says when files can't be read once,
// not at every scan.
func TestStatusShowsWhatScansFound(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs file permissions to be enforced, and symlinks")
	}
	n := openNode(t, "Mac")
	rec := &recorder{}
	n.Log = slog.New(rec)
	n.PackageSettle = time.Hour
	dir := library(t, n, "notes")
	writeFile(t, dir, "secret.md", "unreadable")
	must(t, os.Chmod(filepath.Join(dir, "secret.md"), 0))
	must(t, os.Symlink("secret.md", filepath.Join(dir, "link.md")))
	writeFile(t, dir, "Talk.key/Index.zip", "just written")
	for range 2 {
		_, err := n.ScanLibrary("notes")
		must(t, err)
	}
	l := n.Status().Libraries[0]
	if len(l.ScanErrors) != 1 || !strings.HasPrefix(l.ScanErrors[0], "secret.md: ") ||
		!slices.Equal(l.Skipped, []string{"link.md (symlink)"}) || !slices.Equal(l.Busy, []string{"Talk.key"}) {
		t.Fatalf("scan errors %q, skipped %q, busy %q", l.ScanErrors, l.Skipped, l.Busy)
	}
	if got := count(rec.messages(), "WARN couldn't read some files"); got != 1 {
		t.Fatalf("logged that files can't be read %d times over two scans", got)
	}
}

// A library lists only the devices it syncs with.
func TestStatusListsTheLibrarysDevices(t *testing.T) {
	a, b, c := openNode(t, "Mac"), openNode(t, "iPhone"), openNode(t, "iPad")
	must(t, Pair(a, b))
	must(t, Pair(a, c))
	library(t, a, "notes", c.Device.DeviceID)
	library(t, a, "all")
	for _, l := range a.Status().Libraries {
		var names []string
		for _, d := range l.Devices {
			names = append(names, d.Name)
		}
		slices.Sort(names)
		want := map[string][]string{"notes": {"iPad"}, "all": {"iPad", "iPhone"}}[l.ID]
		if !slices.Equal(names, want) {
			t.Errorf("%s lists %q; want %q", l.ID, names, want)
		}
	}
}

// After a sync fails, the library waits before it tries that device
// again, twice as long after each failure. Syncing everything doesn't
// wait.
func TestFailedSyncsWaitToRetry(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	// Paired without a network, B has no address for A.
	must(t, Pair(a, b))
	library(t, b, "notes")
	k := syncPair{a.Device.DeviceID, "notes"}
	failures := func() (f int) {
		locked(b, func() { f = b.synced[k].failures })
		return f
	}
	age := func(d time.Duration) {
		locked(b, func() {
			m := b.synced[k]
			m.failedAt = m.failedAt.Add(-d)
			b.synced[k] = m
		})
	}
	if err := b.SyncChanged(); err == nil || failures() != 1 {
		t.Fatalf("a sync with no address: %v, %d failures", err, failures())
	}
	must(t, b.SyncChanged())
	if failures() != 1 {
		t.Fatal("tried again at once")
	}
	age(retryMin)
	if err := b.SyncChanged(); err == nil || failures() != 2 {
		t.Fatalf("didn't try again after %v", retryMin)
	}
	age(retryMin)
	must(t, b.SyncChanged())
	if failures() != 2 {
		t.Fatal("tried again before the wait doubled")
	}
	if err := b.SyncAllPeers(); err == nil || failures() != 3 {
		t.Fatal("syncing everything waited for the retry")
	}
}

func TestRetryAfter(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		1:    retryMin,
		2:    2 * retryMin,
		3:    4 * retryMin,
		6:    32 * retryMin,
		7:    retryMax,
		1000: retryMax,
	} {
		if got := retryAfter(failures); got != want {
			t.Errorf("retryAfter(%d) = %v; want %v", failures, got, want)
		}
	}
}

// Activity shows a sync in progress without waiting for the node's lock,
// and the sync counts as failed when the other device hangs up.
func TestActivityShowsSyncs(t *testing.T) {
	a, b := openNode(t, "Mac"), openNode(t, "iPhone")
	must(t, Pair(a, b))
	library(t, b, "notes")
	hangUp := make(chan struct{})
	addr, _ := acceptAs(t, a, func(c *tls.Conn) {
		_, _ = transport.ReadMsg(c)
		<-hangUp
	})
	synced := make(chan error, 1)
	go func() { synced <- b.SyncWith(addr, "notes") }()
	want := Task{Library: "notes", Doing: Syncing, DeviceID: a.Device.DeviceID}
	for deadline := time.Now().Add(5 * time.Second); !hasTask(activityLocked(t, b), want); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("Activity shows %+v during a sync", b.Activity())
		}
	}
	close(hangUp)
	if err := <-synced; err == nil {
		t.Fatal("a sync the other device hung up on worked")
	}
	if got := b.Activity(); len(got) != 0 {
		t.Fatalf("Activity shows %+v after the sync ended", got)
	}
	if d := deviceSync(t, b, "notes", a); d.Failures != 1 || d.Error == "" {
		t.Fatalf("after the other device hung up: %+v", d)
	}
}

// Activity shows scans and applies while they run.
func TestActivityShowsScansAndApplies(t *testing.T) {
	n := openNode(t, "Mac")
	dir := library(t, n, "notes")
	writeNotes(t, dir, 0, 2*brakeMin)
	_, err := n.ScanLibrary("notes")
	must(t, err)
	removeNotes(t, dir, 0, 2*brakeMin)
	// The scan logs the deletes it holds as it runs.
	var during []Task
	n.Log = slog.New(&recorder{handle: func(slog.Record) { during = n.Activity() }})
	_, err = n.ScanLibrary("notes")
	must(t, err)
	if !hasTask(during, Task{Library: "notes", Doing: Scanning}) {
		t.Fatalf("Activity shows %+v during a scan", during)
	}
	if got := n.Activity(); len(got) != 0 {
		t.Fatalf("Activity shows %+v after the scan", got)
	}
	n.Log = slog.New(slog.DiscardHandler)

	other := openNode(t, "iPhone")
	writeFile(t, library(t, other, "notes"), "new.md", "from the iPhone")
	remote, err := other.ExportIndex("notes")
	must(t, err)
	var once sync.Once
	fetching := make(chan struct{})
	fetched := make(chan struct{})
	pulled := make(chan error, 1)
	go func() {
		pulled <- n.PullFrom(remote, func(h string) ([]byte, error) {
			once.Do(func() { close(fetching) })
			<-fetched
			return other.GetBlob(h)
		})
	}()
	<-fetching
	// The apply holds the node's lock.
	if got := activityWithin(t, n); !hasTask(got, Task{Library: "notes", Doing: Applying}) {
		t.Errorf("Activity shows %+v during an apply", got)
	}
	close(fetched)
	must(t, <-pulled)
	wantFile(t, dir, "new.md", "from the iPhone")
	if got := n.Activity(); len(got) != 0 {
		t.Fatalf("Activity shows %+v after the apply", got)
	}
}

// What goes wrong is logged when it starts and when it stops, not at
// every scan.
func TestProblemsAreLogged(t *testing.T) {
	n := openNode(t, "Mac")
	rec := &recorder{}
	n.Log = slog.New(rec)
	dir := library(t, n, "notes")
	writeNotes(t, dir, 0, 2*brakeMin)
	scan := func() {
		t.Helper()
		_, _ = n.ScanLibrary("notes")
	}
	scan()
	removeNotes(t, dir, 0, 2*brakeMin)
	scan()
	scan()
	_, err := n.ConfirmDeletes("notes", nil)
	must(t, err)
	must(t, os.RemoveAll(dir))
	scan()
	scan()
	must(t, os.MkdirAll(dir, 0o700))
	scan()
	scan()

	other := openNode(t, "iPhone")
	must(t, Pair(n, other))
	_ = n.SyncAllPeers()
	_ = n.SyncAllPeers()
	writeFile(t, library(t, other, "notes"), "hello.md", "from the iPhone")
	addr := listen(t, other)
	locked(n, func() { n.Config.PeerByID(other.Device.DeviceID).Addresses = []string{addr} })
	must(t, n.SyncAllPeers())

	got := rec.messages()
	for msg, want := range map[string]int{
		"WARN holding deletes until they're confirmed": 1,
		"WARN scan failed":     1,
		"INFO scanning again":  1,
		"WARN sync failed":     2,
		"INFO syncing again":   1,
		"INFO applied changes": 1,
	} {
		if n := count(got, msg); n != want {
			t.Errorf("logged %q %d times; want %d. The log:\n%s", msg, n, want, strings.Join(got, "\n"))
		}
	}
}

func deviceSync(t *testing.T, n *Node, libID string, other *Node) DeviceSync {
	t.Helper()
	for _, l := range n.Status().Libraries {
		for _, d := range l.Devices {
			if l.ID == libID && d.DeviceID == other.Device.DeviceID {
				return d
			}
		}
	}
	t.Fatalf("%s's status doesn't list %s for %s", n.Device.Name, other.Device.Name, libID)
	return DeviceSync{}
}

// activityLocked is n's Activity while the test holds n's lock.
func activityLocked(t *testing.T, n *Node) (tasks []Task) {
	t.Helper()
	locked(n, func() { tasks = activityWithin(t, n) })
	return tasks
}

// activityWithin is n's Activity, which must answer at once.
func activityWithin(t *testing.T, n *Node) []Task {
	t.Helper()
	got := make(chan []Task, 1)
	go func() { got <- n.Activity() }()
	select {
	case tasks := <-got:
		return tasks
	case <-time.After(5 * time.Second):
		t.Fatal("Activity waited for the node's lock")
		return nil
	}
}

// hasTask reports whether tasks has want, started at any time.
func hasTask(tasks []Task, want Task) bool {
	return slices.ContainsFunc(tasks, func(t Task) bool {
		t.Since = time.Time{}
		return t == want
	})
}

func count(lines []string, line string) (n int) {
	for _, l := range lines {
		if l == line {
			n++
		}
	}
	return n
}

// recorder is a slog handler that keeps each record's level and message,
// and hands the record to handle.
type recorder struct {
	mu     sync.Mutex
	lines  []string
	handle func(slog.Record)
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	r.lines = append(r.lines, fmt.Sprintf("%s %s", rec.Level, rec.Message))
	r.mu.Unlock()
	if r.handle != nil {
		r.handle(rec)
	}
	return nil
}

func (r *recorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *recorder) WithGroup(string) slog.Handler      { return r }

func (r *recorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}
