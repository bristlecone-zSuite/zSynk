# Threat model

Synk assumes the user's documents are more valuable than convenience. The engine is biased toward extra copies, not extra deletes.

This document says who can attack a Synk device, what they can try, and what stops them. It follows the code: every claim here is one a test or the code itself backs. Where a limit remains, it says so.

## Assets

- Live library files (Obsidian vaults, iWork packages, Ulysses sheets).
- The version vault (previous versions of every overwrite/delete).
- Device identity keys.
- Pairing sessions (offers, codes, tokens).

## Trust boundaries

A Synk device trusts, in decreasing order:

1. **The user's own account.** Anything running as the user can read the data folder, the API token in it, and the keys. This is the base trust; Synk can't defend against it (see "Other local users").
2. **Paired devices.** After a pairing exchange, a device holds the other's public key and syncs the libraries shared with it.
3. **The local network.** Unpaired devices on the same network can connect to the peer port but prove nothing, and get refused.
4. **Everything else**, including the internet: the peer port refuses addresses it doesn't consider local unless `allowInternetPeers` is set, and the API listens on loopback only.

## Adversaries and faults

### A web page in a browser

A page runs in a browser on the same Mac and wants to drive the local API at `127.0.0.1:7421`: enable a library pointing anywhere, start a pairing that trusts the attacker's device, or read the status.

What stops it, in `internal/api/server.go` (`guard`):

- The API requires a bearer token, created at startup and kept with mode 0600 in the data folder. A page can't read it, and a "simple" cross-origin POST can't carry an `Authorization` header.
- Requests whose `Host` header isn't `127.0.0.1:<port>` or `localhost:<port>` are refused, so DNS rebinding can't point a browser at the API and read replies.
- Any request carrying an `Origin` header is refused. Browsers attach one to every cross-origin request, and to preflights, so a page can neither send a JSON POST nor read a response.
- POST bodies must be `application/json`; a form or `text/plain` POST is refused before the body is decoded.
- Every route is registered with its method; a `GET` can't reach a handler that changes anything.
- The socket listens on loopback only; `Listen` refuses anything else.

What remains: a page can always ask `/v1/health`, which answers only whether the daemon is up, with no data. A browser extension or a page the user has granted localhost access to is still stopped by the token.

### Another local user, or another app on the same Mac

MacOS accounts other than the user's, and sandboxed apps with network access, can reach loopback ports.

- **The API:** the token file is mode 0600 in the user's data folder, and `LoadOrCreateToken` replaces a token file other users could read. Another user knows none of the hosts, codes, or tokens, so every API answer is 401.
- **The peer port:** connecting is possible, but a session needs a key the daemon has paired with. An unpaired connection gets one pre-authentication message, at most 64 KiB, in 10 seconds, and then a refusal. Concurrent unauthenticated connections are capped (64 in all, 8 per host), so they can't exhaust memory.
- **The files:** library files and the data folder carry the user's own permissions. What another user can do to them is what macOS already allows, and Synk neither widens nor narrows that.

What remains: malware running **as the user** can do anything Synk can — read the token, read the libraries, run the CLI. Synk doesn't try to contain a compromised user account; no user-level program can.

### An attacker on the network path

Someone on the same Wi-Fi, or any router between two paired devices, can read, record, alter, replay, or replace traffic, and can try to sit in the middle of a pairing.

What stops it, in `internal/transport` and `internal/engine/session.go`:

- **Confidentiality and integrity.** Every connection is TLS 1.3 with the ALPN protocol `synk/2`, and session tickets are off, so nothing is carried over an old session. Each end presents its device's Ed25519 key in a self-signed certificate, and each end refuses any other key. All bytes, including indexes, want lists, blobs, and pairing tokens, are inside that channel.
- **Impersonation.** A sync connection is accepted only from a key already on the paired list; pairing connections are accepted only with a valid proof (below). A device ID shown in the UI is derived from the key, so a connection's identity is the key itself, matched by the whole key, not by the readable ID.
- **Replay and relay of pairing proofs.** Each side proves it knows the offer's code and token with an HMAC that covers the TLS exporter binding of that one connection (`docs/PROTOCOL.md`). A device in the middle holds two different TLS sessions, so a proof captured on one can't be replayed on the other.
- **Man in the middle of a pairing.** The offer link (`synk://pair?…`) carries the offering device's full key fingerprint, and the joining device refuses any other key. Typed pairings have no fingerprint to pin, so there the proofs carry the binding, which keeps a middle out as long as the token stays secret; `PROTOCOL.md` says when each applies.
- **Tampering with content.** Content is addressed by SHA-256 everywhere: a peer can only send bytes whose hash the receiving device asked for and verified. Bytes that don't match go to quarantine, never into a library.
- **Framing attacks.** Pre-authentication frames are capped at 64 KiB, bodies at limits set by the index that names them, and every allocation the peer can influence is bounded before the peer is trusted.

What remains: an attacker on the path can always refuse service (drop or stall connections). A device that paired by typing a code is safe against a middle only while the token stays secret; if the user reads the token out loud on an untrusted channel, that pairing is between the user and whoever holds it.

### A compromised paired device

A device the user legitimately paired with, whose software later turns hostile, or is stolen. This is the strongest adversary inside Synk's boundary, and the one the October 2026 assessment was most worried about.

What stops it:

- **Confinement.** Paths that arrive from a peer are validated (`safepath.Check`) and every file operation goes through an `os.Root` opened on the receiving library. A peer cannot name a path outside that library, inside `.synk`, or through a symlink; writes land in the library or nowhere.
- **Least content.** A device serves only content its own index referenced, and only to a device the library is shared with (`ShareWith` is enforced on both sides). A peer cannot pull another library's bytes.
- **Bounded damage per connection.** Wants are limited to blobs the received index needs; unsolicited blobs end the session; sizes are bounded by the index; received bytes are hash-verified before storage.
- **The vault.** Every overwrite and delete takes a vault snapshot first, and a snapshot failure aborts the change (rule 4). So even a peer that deletes everything shared with it leaves the previous bytes in the receiving device's vault, restorable with `synk restore`.

What remains, said plainly:

- A compromised paired device can **change or delete anything in the libraries it is shared with**, on every device that shares them. Pairing is the trust boundary; this is inherent to sync. The vault makes it recoverable, not preventable.
- It can exhaust disk or connections within the limits above, which shows up as failed syncs, not lost data.

The mass-delete brake (a scan that would delete more than 50 files, or a tenth of a library, holds them for confirmation) applies to deletes a device's **own** scan finds. A delete that arrives from a peer is snapshotted and applied; putting the same brake on the receiving side is a possible later hardening, recorded as a limit here until then.

## Out of scope (for now)

- A compromised user account (malware running as the user). See "Other local users".
- End-to-end encryption of file bytes at rest beyond OS file permissions (0600 on keys and blobs). Full vault encryption is a later layer.
- Multi-user sharing. Synk is for one person's machines.
- Real-time collaborative editing inside a single document.
- Traffic analysis: sizes and timings of syncs are visible to anything on the path, encrypted or not.
- The relay: no daemon relay client exists yet (Phase 6). Until then Synk connects only to addresses it learned from pairing or the offer, on the local network. When the relay lands, this document and `docs/PROTOCOL.md` get its adversary: the relay operator, who must be able to see only ciphertext and room membership.

## Recovery

`synk doctor` reports missing folders, unpaired state, and placeholder hits. The vault under `$SYNK_DIR/vault/versions` holds previous snapshots. Restoring is `synk versions` and `synk restore`; we never "heal" by deleting extras.
