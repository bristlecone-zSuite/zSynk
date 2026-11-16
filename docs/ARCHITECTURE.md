# Architecture

Synk is a local-first, paired-device sync system. There is no required cloud account. A device is trusted only after an explicit pairing code/QR exchange.

```
┌──────────────────────────────────────────────┐
│  Mac menu bar / iOS app                      │
│  install → name device → pick apps → pair   │
└──────────────────┬───────────────────────────┘
                   │ localhost HTTP (loopback only)
┌──────────────────▼───────────────────────────┐
│  synk daemon                                 │
│  presets · pairing · doctor                  │
└──────┬───────────────┬──────────────────────┘
       │                 │
       ▼                 ▼
  safety vault      sync engine
  (versions,        (scan → version vectors
   quarantine,       → reconcile → apply)
   journal)
```

## Data path

1. **Scan** the library. Packages are one entry. `.icloud` files are incomplete, never tombstones. Ignore lists hide caches, not user notes.
2. **Advance** the folder index. A tombstone is created only if a path was live in the last committed index and is confirmed gone.
3. **Reconcile** with a peer’s index using version vectors, not clocks.
   - Remote dominates → take remote.
   - Local dominates → keep local.
   - Concurrent edits → deterministic winner at the original path plus a `.synk-conflict-<hash>` copy so both versions survive on both devices.
   - Concurrent delete vs edit → **the edit wins**.
4. **Fetch blobs** by SHA-256. Bytes that do not hash as claimed go to quarantine and are not written.
5. **Apply** through a journal: start → snapshot old bytes → prepare staging → fsync → rename → commit. A crash during prepare rolls back; a crash after prepare finishes the rename.

## Packages

A `.pages` / `.key` / `.numbers` / `.textbundle` / `.scriv` directory is hashed by a canonical manifest of interior files. Materialization writes a complete staging tree, verifies the manifest, then swaps the directory. The host app never sees a half-written bundle.

## Pairing

Each device has an Ed25519 identity. The device ID is a Crockford-base32 fingerprint of the public key. Pairing exchanges names and public keys after a short code plus a high-entropy token (the QR carries the token; typing the code alone is not enough on a hostile LAN). `synk pair-offer` writes the offer to `$SYNK_DIR/pairing.json` so a separately running `synk serve` can accept the join. Peer frames (hello, index, pair-join) are signed with the device key and carry a timestamp; unsigned or skewed frames are rejected.

The joiner advertises its listen *port*; the acceptor stores `remoteIP:listenPort`, not the ephemeral TCP source port.

## Relays

LAN is TCP on port 7420. When NAT would block a path, devices can use an optional dumb relay that splices already-encrypted frames (see `relay/`). The relay cannot read file bytes. If a future build uses Syncthing’s relay network, this component can be skipped.

## iOS

iOS cannot run a full daemon in the background. The companion app:

- Runs the same engine while foregrounded and during granted background tasks.
- Presents libraries through a File Provider so Obsidian, iA Writer, Pages, and Keynote open files from **On My iPhone → Synk**.
- Treats a Mac (or any always-on paired device) as the usual catch-up peer. Two iPhones with no Mac in the middle will sync when both apps are open.

That is honest. “Continuous iOS sync while the phone is in a pocket” is an Apple limitation, not a configuration problem.

## What the user sees

1. Name this Mac.
2. Tap the apps to keep in sync.
3. Scan a QR on the other device.
4. Green status. Conflicts, if any, are extra files with obvious names — nothing is silently chosen.
