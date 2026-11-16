package engine

import (
	"time"
)

// Event is one thing that happened on this device that an app may want
// to show while it happens: a sync that failed, deletes waiting to be
// confirmed, a device that paired, a library that changed state.
type Event struct {
	At time.Time `json:"at"`
	// Kind is scan, sync, deletes, pairing, library, or peer.
	Kind    string `json:"kind"`
	Library string `json:"library,omitempty"`
	Device  string `json:"device,omitempty"`
	// Level is info, or warning for what needs a person.
	Level   string `json:"level,omitempty"`
	Message string `json:"message"`
}

// eventKeep is how many events a subscriber that reads slowly can catch
// up on. Events an app misses are in the log and in status; the stream
// is for reacting while they happen.
const eventKeep = 64

// Subscribe returns the events that happen from now on, and the call
// that stops the flow. A subscriber that reads too slowly loses events
// rather than slowing Synk down.
func (n *Node) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, eventKeep)
	n.eventsMu.Lock()
	if n.subs == nil {
		n.subs = make(map[chan<- Event]bool)
	}
	n.subs[ch] = true
	n.eventsMu.Unlock()
	return ch, func() {
		n.eventsMu.Lock()
		delete(n.subs, ch)
		n.eventsMu.Unlock()
	}
}

// publish hands an event to every subscriber without waiting for one.
func (n *Node) publish(kind, library, device, level, message string) {
	e := Event{At: time.Now().UTC(), Kind: kind, Library: library, Device: device, Level: level, Message: message}
	if e.Level == "" {
		e.Level = "info"
	}
	n.eventsMu.Lock()
	defer n.eventsMu.Unlock()
	for ch := range n.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
