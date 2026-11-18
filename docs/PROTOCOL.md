# Peer protocol (synk/2)

This is how two Synk devices pair and sync a library. The code is in `internal/transport` (framing and TLS), `internal/engine/session.go` (sessions), `internal/pairing` (offers and proofs), and `internal/reconcile` (indexes).

synk/2 hasn't shipped, so it can still change under the same name. Once it ships, a change that an older device can't read needs a new ALPN name (see "Versions").

## Connections

- Peers talk over TCP. A device listens on the `listen` address in its `config.json`, port 7420 by default.
- Every connection is TLS 1.3, and both ends must negotiate the ALPN protocol `synk/2`. Session tickets are off, so every connection proves both keys again.
- Each end presents exactly one self-signed X.509 certificate, holding the device's Ed25519 identity key. Nothing checks the certificate's names, dates, or issuer. Devices pin each other's whole public key instead.
- The device that dials checks the other device's key before it sends its own certificate:
  - for a sync, the key must belong to a paired device;
  - for pairing with a link, the key's fingerprint must match the link's `fp`.
- The device that answers accepts any key during the handshake, then reads one message. A `pair-join` is checked against its pairing offer. A `hello` is answered only for a paired key. Anything else is refused.
- Until that first message is read, the answering device allows 10 seconds for the handshake and the message, and the message may be at most 64 KiB.
- A device takes at most 64 connections at once, and 8 from any one host. It accepts connections only from loopback, link-local, private, and shared (100.64.0.0/10) addresses, and from subnets of its own interfaces, unless `config.json` sets `allowInternetPeers`.
- Once a session is underway, it ends after 5 minutes without progress in either direction. There's no limit on how long a session that keeps moving can take.

A device's ID, such as `QQKX-AHEH-BZYC-WDA9`, is the first 80 bits of the SHA-256 of its public key, in Crockford base32, in four groups of four. The ID is for people to read; peers are always matched by the whole key.

## Framing

- A frame is a 4-byte big-endian length followed by that many bytes.
- A message is one frame holding a JSON object, at most 1 MiB.
- Some messages are followed by a body: the message's `size` is the body's length in bytes, and the body follows in frames of 1 byte to 1 MiB each, adding up to exactly `size`. A message with no body, such as an error, has an empty one.

| Field | Used by | Meaning |
| --- | --- | --- |
| `kind` | all | One of the kinds below |
| `name` | `pair-join`, `pair-ok` | The sender's device name, at most 64 printable characters as shown |
| `folderId` | `hello`, `index` | The library's ID |
| `listenPort` | `hello`, `pair-join`, `pair-ok` | The port the sender listens on, so the other device can dial it back |
| `proof` | `pair-join`, `pair-ok` | The pairing proof |
| `hash` | `blob` | The blob's SHA-256, as 64 lowercase hex digits |
| `size` | `index`, `want`, `blob` | The length of the body that follows |
| `offset` | `blob` | How many bytes of the blob the receiver already holds, so the body starts there. Absent when the transfer starts from the first byte |
| `error` | `error` | Why the sender stopped. It's shown to the user, cleaned to one line of at most 200 printable characters |

| Kind | Body | Sent |
| --- | --- | --- |
| `pair-join` | none | First message of a pairing |
| `pair-ok` | none | Pairing accepted |
| `hello` | none | First message of a sync |
| `hello-ok` | none | The library is shared with the device that dialed |
| `index` | The sender's index, as JSON | Once by each device |
| `want` | A JSON array of `{hash, offset}` objects: one blob per object, with `offset` saying how many bytes of it the asking device already holds | Once by each device |
| `blob` | The blob's content, from the asked offset on | Once for each hash asked for |
| `done` | none | The answering device applied the changes |
| `error` | none | Instead of any reply. The connection then closes |

## Pairing

An offer has four parts:

- a code of 8 Crockford base32 characters, shown as `YN9T-BMTZ`;
- a token of 128 random bits, as 32 hex digits;
- the offering device's key fingerprint: the SHA-256 of its raw 32-byte public key, as 64 hex digits;
- the addresses the offering device listens on.

An offer pairs one device and expires after an hour. The link that a QR code carries has one `addr` for each address, and its parameters can come in any order:

```
synk://pair?addr=192.168.1.20%3A7420&addr=%5Bfd00%3A%3A20%5D%3A7420&code=YN9T-BMTZ&fp=bde7…&token=8eb1…&v=2
```

A device that reads a link with a `v` other than `2` refuses it.

Each side proves it knows the offer, bound to this one connection:

```
binding = TLS exporter, label "EXPORTER-synk-binding", no context, 32 bytes
proof   = hex(HMAC-SHA256(key = token bytes,
                          "synk/2 pairing " || role || 0x00 || code || 0x00 || binding))
```

Here `role` is `join` or `accept`, and `code` is upper case with dashes and spaces removed.

1. The joining device dials the offer's addresses in turn until one answers, refusing any key whose fingerprint isn't the link's `fp`. It sends `pair-join` with its name, its listen port, and its `join` proof.
2. The offering device checks the proof against its offer, saves the joining device's key and name, and saves the address it connected from, with the port the joining device gave. It answers `pair-ok` with its own name, listen port, and `accept` proof, or with `error`.
3. The joining device checks the `accept` proof, then saves the offering device's key and name, and the address it dialed.

Because the proofs cover the TLS binding, a device in the middle can't relay or replay them: it would hold two different TLS sessions. Without `fp`, when the code and token are typed instead of scanned, that keeps out a device in the middle as long as the token stays secret.

## Relay

Two devices that cannot reach each other directly can meet through a
relay. The relay is a dumb splice: it forwards opaque binary frames
between exactly two connections and never reads a byte of payload.
Everything above — the TLS 1.3 handshake, the certificate pinning, the
framing — runs end to end between the peers as if they were directly
connected; the relay only carries the bytes.

The relay speaks a small WebSocket dialect. A peer dials
`ws://…/v1/rooms/<room>` (or `wss://`) and upgrades. Binary frames
carry the bytes in each direction, at most 1 MiB plus 64 KiB of
headroom per frame. A text frame, a frame over the size cap, or a
protocol violation ends the room with close code 1003, 1009, or 1002.
A room holds its first peer for the relay's wait period until the
second arrives, then closes with 1001 if it doesn't, and a peer that
arrives after the room ended is refused with 1001. When one peer
leaves, the relay ends the room and closes the other side with the
leaving peer's close code, or 1000.

The room name is 64 lowercase hex digits, derived by both peers from
information they already hold (decision 0012):

```
secret = hex(SHA-256(min(kA,kB) || 0x00 || max(kA,kB)))
room   = hex(SHA-256("synk/2 relay" || 0x00 || min(iA,iB) || 0x00
                                  || max(iA,iB) || 0x00 || secret))
```

`kA` and `kB` are the peers' raw 32-byte public keys and `iA` and `iB`
their printable device IDs, in each case ordered by bytes.

The room name is a rendezvous label, not an authentication factor:
knowing it lets someone occupy a slot in the room, and the pinned TLS
handshake still decides who the peers are.

## Sync sessions

A session syncs one library. In the steps below, D is the device that dials, and A is the device that answers.

1. D sends `hello` with the library's ID and its listen port.
2. A answers `hello-ok`, or `error` if the library isn't shared with D or is paused.
3. A scans the library and sends its `index`.
4. D sends its `index`.
5. D sends `want`, listing the blobs it needs from A.
6. A sends a `blob` for each, in the order asked.
7. A sends `want`, and D sends its blobs.
8. Both devices apply what they received, at the same time.
9. A sends `done`, or `error` if it couldn't apply the changes.

Either device may send `error` in place of its next message, or close the connection, and the session ends. A device that wants nothing sends an empty list. A also saves D's address (D's IP and listen port) for when it dials D.

Wants and blobs follow these rules. A device that breaks one ends the session.

- A device asks only for blobs it lacks and needs in order to apply the other device's changes: a file's content, or a package's manifest and parts.
- A device sends only blobs referenced by the index it sent, each at most once. A want list may be no longer than the number of distinct blobs that index references.
- A blob may be no larger than the sender's index allows: a file's size, a package's size for each of its parts, and 64 MiB for a manifest. A `want` with an offset asks for the bytes from that offset on, so the message's `size` and the body together cover at most what the index allows. The receiver also keeps 64 MiB of disk free.
- A device that asks with an offset holds those bytes itself, staged from a transfer that stopped part way. The receiver checks the whole blob's SHA-256, staged bytes and arriving bytes together, before it stores anything, and discards the staged content if it doesn't match. Staged content lives only in the receiver's own store, and is removed by garbage collection after the same grace period as unreferenced content.

## Indexes

```json
{
  "folderId": "obsidian",
  "deviceId": "QQKX-AHEH-BZYC-WDA9",
  "entries": {
    "Daily/2026-10-07.md": {
      "path": "Daily/2026-10-07.md",
      "kind": "file",
      "hash": "8b2e14aa…",
      "size": 1934,
      "vector": {"QQKX-AHEH-BZYC-WDA9": 12, "0R9M-7TS0-9B0H-21S0": 3},
      "mtime": 1791409036978846602,
      "by": "QQKX-AHEH-BZYC-WDA9"
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `path` | Relative to the library, with `/` between names. It must be clean, valid UTF-8, without backslashes or NUL, outside `.synk`, and equal to its key |
| `kind` | `file` or `package` |
| `hash` | The SHA-256 of a file's content, or of a package's manifest |
| `size` | A file's size in bytes, or the sum of a package's parts |
| `vector` | The version vector: a counter for each device that changed the path |
| `deleted` | The path was deleted. The entry is a tombstone |
| `incomplete` | This device can't see the path's current content: it's unreadable, an iCloud placeholder, or ignored here. Its content fields mean nothing |
| `parts` | For a package, the hashes of its files, in manifest order |
| `exec` | The file is executable, judged by the owner's bit as Git does. Packages never are |
| `mtime` | The version's modification time on the device that made it, in Unix nanoseconds; 0 if unknown |
| `by` | The device that made the version |

A package's manifest is a blob too: `{"kind":"package","files":[{"path":…,"hash":…,"size":…}]}` with the files sorted by path, as Go's `encoding/json` writes it. The package's hash is the SHA-256 of those bytes.

A device reads at most 1,048,576 entries and 512 MiB of index. It also reads at most 65,536 parts per package, 256 devices per vector, 64 characters per device ID, and counters up to 2^53. Entries that break a rule are dropped. Dropping an entry never deletes anything, because a path missing from an index is never treated as deleted.

## Reconciling

- Version vectors decide which version is newer. A newer version replaces an older one, and a delete replaces a version only if the delete's vector is newer.
- Changing a file's content or executable bit makes a new version. Changing only its time doesn't, so a touch doesn't sync.
- When two versions are concurrent and their content differs, both devices keep both. The winner stays at the path, and the other version goes to `<name>.synk-conflict-<first 8 hex digits of its hash><extension>`. The winner is the version modified later, then the one made by the device whose ID sorts last, then the smaller hash. The time only picks the winner; it never decides which version is newer.
- When two concurrent versions differ only in the executable bit, the winner's bit is kept, and no copy is made.
- An edit concurrent with a delete stays.
- Applying a version gives the file its executable bit and modification time.
- Some file systems don't keep executable bits: FAT, exFAT, NTFS and SMB shares show every file as executable, or none. A library on any file system except APFS, HFS+, ZFS and NFS on macOS, or ext4, XFS, Btrfs, tmpfs, ZFS, F2FS, bcachefs, overlayfs and NFS on Linux, ignores the bits its files show. A path it already knows keeps the bit its index has, and a new file isn't executable.

## Versions

The ALPN name is the protocol version. A device that speaks a later version offers that name first, and `synk/2` after it for as long as it still speaks synk/2. TLS picks the first name both devices know.
