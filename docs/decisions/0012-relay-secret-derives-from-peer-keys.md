# 0012: The relay secret derives from the paired devices' public keys

Date: 2026-11-17

## Context

The self-hosted relay (`relay/`) is a rendezvous point: two peers that
cannot reach each other directly both dial it, name the same room, and
the relay splices their connections. The room name must be computable
by exactly the two peers. Three sources for the secret behind it were
considered:

- a fresh random secret exchanged during pairing. Strongest against an
  adversary who has somehow learned both public keys, but it changes
  the pairing wire format, must be stored in each peer record, and
  every pair made before the relay existed could not use the relay
  without pairing again.
- the pairing token. It is single-use and expires with the offer, so
  it cannot label a long-lived pair.
- a value derived from the two peers' public keys. Both devices hold
  both keys after pairing, so the derivation needs no wire change and
  no extra storage, and it works for every pair that has ever been
  made.

## Decision

- The relay secret is `hex(SHA-256(min(kA,kB) || 0x00 || max(kA,kB)))`,
  where `kA` and `kB` are the peers' raw 32-byte Ed25519 public keys,
  ordered by their bytes. The room name is then
  `relay.RoomName(secret, idA, idB)`: SHA-256 over `synk/2 relay`, the
  two printable device IDs ordered by bytes, and the secret.
- A room name stays a rendezvous label, not an authentication factor.
  Whoever holds both public keys can compute it; that lets them occupy
  a slot in the room, and nothing else. The spliced connection runs
  the full synk/2 handshake end to end: pinned certificates decide
  who the peers are, and only ciphertext crosses the relay.
- The relay itself learns every room name from the URL it serves, so a
  hostile relay can deny a rendezvous by taking a slot first, and can
  do nothing more: it cannot read, inject, or impersonate.

## Consequences

- No pairing wire change and no per-peer storage: every paired pair
  can use a relay, including pairs made before the relay existed.
- An adversary who learns both full public keys can disturb rendezvous
  through the relay (a denial of service), but cannot read, inject, or
  impersonate. The full keys are not transmitted in the clear during a
  sync (TLS 1.3 encrypts the certificate flight), and the pairing link
  carries only the key's fingerprint, not the key.
- If a future protocol needs a stronger label — per-library rooms, for
  example — the secret becomes an input stored per pair, and the relay
  package's API does not change.
