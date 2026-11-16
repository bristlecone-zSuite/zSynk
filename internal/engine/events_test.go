package engine

import (
	"testing"
	"time"
)

// The event stream tells an app what happened as it happens: a sync
// that failed, a device that unpaired.
func TestEvents(t *testing.T) {
	a, b := openNode(t, "A"), openNode(t, "B")
	must(t, Pair(a, b))
	dirA := library(t, a, "notes")
	library(t, b, "notes")
	writeFile(t, dirA, "a.md", "hello")

	events, cancel := a.Subscribe()
	defer cancel()

	// A session that fails publishes a warning with the reason.
	must(t, b.PauseLibrary("notes"))
	if err := a.SyncWith(listen(t, b), "notes"); err == nil {
		t.Fatal("a sync with a paused library succeeded")
	}
	e := waitFor(t, events)
	if e.Kind != "sync" || e.Level != "warning" || e.Library != "notes" {
		t.Fatalf("a failed sync published %+v", e)
	}

	// Membership changes publish what an app would show.
	must(t, a.Unpair(b.Device.DeviceID))
	e = waitFor(t, events)
	if e.Kind != "peer" || e.Device != b.Device.DeviceID {
		t.Fatalf("unpairing published %+v", e)
	}
}

// A subscriber that stops reading no longer receives anything, and
// publishing never waits for one.
func TestSubscribeStopsWithCancel(t *testing.T) {
	n := openNode(t, "A")
	library(t, n, "notes")
	events, cancel := n.Subscribe()
	cancel()
	must(t, n.PauseLibrary("notes"))
	must(t, n.ResumeLibrary("notes"))
	select {
	case e := <-events:
		t.Fatalf("a cancelled subscription still received %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// A subscriber that doesn't read loses events instead of slowing Synk
// down, and the newest ones stay available up to the buffer's size.
func TestSlowSubscriberLosesEvents(t *testing.T) {
	n := openNode(t, "A")
	library(t, n, "notes")
	events, cancel := n.Subscribe()
	defer cancel()
	for i := 0; i < eventKeep+32; i++ {
		must(t, n.PauseLibrary("notes"))
		must(t, n.ResumeLibrary("notes"))
	}
	// Publishing blocked nothing; reading now yields buffered events
	// without hanging.
	got := 0
	for {
		select {
		case <-events:
			got++
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	if got == 0 || got > eventKeep {
		t.Fatalf("a slow subscriber received %d events", got)
	}
}

func waitFor(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived")
		return Event{}
	}
}
