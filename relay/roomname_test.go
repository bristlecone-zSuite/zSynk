package relay_test

import (
	"testing"

	"github.com/bristlecone-zSuite/zSynk/relay"
)

// A pair's rooms are separated by a topic, the library ID for a sync
// session: two libraries syncing with the same peer through the relay at
// the same time must not splice into each other's room (decision 0013).
func TestRoomNameHasTopic(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	if relay.RoomName(secret, "B-DEV", "A-DEV", "notes") != relay.RoomName(secret, "A-DEV", "B-DEV", "notes") {
		t.Fatal("the room name depends on argument order")
	}
	if relay.RoomName(secret, "A-DEV", "B-DEV", "notes") == relay.RoomName(secret, "A-DEV", "B-DEV", "photos") {
		t.Fatal("two topics share a room")
	}
}
