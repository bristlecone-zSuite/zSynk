# 0013. The relay room name includes the library

Date: 2026-11-17

## Context

The relay connects two devices by handing each a connection spliced to the
other's, matched on the room both dial. The room name derived from the
pair's secret and the two device IDs (decision 0012), so everything a pair
syncs shared one room. Two devices can sync several libraries at once; each
library is its own sync session. With a single room, a session for one
library could be spliced to a session for another, and the two sessions
would then talk past each other's hello.

## Decision

The room name takes a fourth input, the topic: the ID of the library being
synced. `relay.RoomName(secret, myID, peerID, topic)` folds the topic into
the hash after the secret, so a pair gets one room per library, and two
libraries with the same peer never land in the same room. `relay.Dial`
takes the topic too; the engine passes the folder ID of the library the
session is for.

## Consequences

- A pair's concurrent relay sessions cannot cross-splice; the room name
  mismatch would only ever leave a session waiting, never paired with the
  wrong session.
- The room name is again a rendezvous label only: TLS with the pinned peer
  keys still decides who a connection is to, so a session dialed into a
  room whose other occupant holds the right keys is a genuine peer session
  for that library.
- The room name changed for every library, including the previously only
  conceivable one. Nothing deployed depends on the relay yet, so this
  costs nothing now and would have cost a migration later.
- PROTOCOL.md documents the room name as secret, device IDs, and library
  ID.
