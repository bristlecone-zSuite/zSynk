# Threat model

Synk assumes the user’s documents are more valuable than convenience. The engine is biased toward extra copies, not extra deletes.

## Assets

- Live library files (Obsidian vaults, iWork packages, Ulysses sheets).
- The version vault (previous versions of every overwrite/delete).
- Device identity keys.
- Pairing sessions.

## Adversaries and faults

| Fault | Response |
| --- | --- |
| Concurrent edits | Both versions kept; original path is a deterministic winner |
| Concurrent delete + edit | Edit wins |
| Process crash mid-write | Journal: incomplete transactions abort or finish a verified rename |
| Disk full | Apply refuses before touching the library (needs payload + 64MiB reserve) |
| Bitrot / lying peer | SHA-256 on every blob; mismatch → quarantine, no write |
| iCloud eviction | `.icloud` placeholders are incomplete; never become deletes |
| Torn iWork package | Package is one hash; apply is an atomic directory swap |
| Clock skew | Version vectors, not mtimes, decide causality |
| Partial scan / permission error | Path marked incomplete; no tombstone |
| Unpaired device | Peer connections rejected |
| Impersonation after pairing | Hello/index/pair-join signed with Ed25519; timestamped |
| Loopback API abuse | API binds loopback and rejects non-loopback remotes |
| Silent overwrite | Previous bytes snapshotted to the vault first |
| Ping-pong conflict copies | Conflict path is a function of the losing hash; both peers compute the same names |

## Out of scope (for now)

- A malicious *paired* device. Pairing is the trust boundary. Unpair and restore from the vault if a device is stolen.
- End-to-end encryption of file bytes at rest beyond OS file permissions (0600 on keys and blobs). Full vault encryption is a later layer.
- Multi-user sharing. Synk is for one person’s machines.
- Real-time collaborative editing inside a single document.

## Recovery

`synk doctor` reports missing folders, unpaired state, and placeholder hits. The vault under `$SYNK_DIR/vault/versions` holds previous snapshots. Restoring is a copy from that tree; we never “heal” by deleting extras.
