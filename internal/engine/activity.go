package engine

import (
	"slices"
	"sync"
	"time"
)

// What a Task can be doing.
const (
	Scanning = "scanning"
	Applying = "applying"
	Syncing  = "syncing"
)

// Task is something a node is doing for a library.
type Task struct {
	Library string `json:"library"`
	// Doing is Scanning, Applying, or Syncing.
	Doing string `json:"doing"`
	// DeviceID is the device a sync is with.
	DeviceID string    `json:"deviceId,omitempty"`
	Since    time.Time `json:"since"`
}

// tasks tracks what a node is doing. It has its own lock, so Activity
// answers while a scan or an apply holds the node's.
type tasks struct {
	mu   sync.Mutex
	seq  int
	list []runningTask
}

type runningTask struct {
	id int
	Task
}

// begin adds t to what the node is doing, until end is called.
func (ts *tasks) begin(t Task) (end func()) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.seq++
	id := ts.seq
	t.Since = time.Now().UTC()
	ts.list = append(ts.list, runningTask{id, t})
	return func() {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		ts.list = slices.DeleteFunc(ts.list, func(r runningTask) bool { return r.id == id })
	}
}

// Activity lists what the node is doing now, in the order it started.
// Unlike Status, it doesn't wait for scans and applies to finish.
func (n *Node) Activity() []Task {
	n.tasks.mu.Lock()
	defer n.tasks.mu.Unlock()
	out := make([]Task, 0, len(n.tasks.list))
	for _, r := range n.tasks.list {
		out = append(out, r.Task)
	}
	return out
}
