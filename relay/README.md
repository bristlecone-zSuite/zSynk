# Optional relay (not wired in yet)

The Worker in `relay/worker` deploys and splices two WebSockets per room, but **the daemon has no relay client yet**. The `relay` field in `config.json` is ignored, and devices only sync over direct TCP connections (the LAN, or any address the peers can reach).

## How it is meant to work

1. Both devices derive the same room name: 64 lowercase hex characters (256 bits) computed from a secret they share after pairing.
2. Each device opens `wss://<worker>/v1/rooms/<room>`. The room's Durable Object accepts at most two sockets and forwards binary messages between them. When either side leaves, both are closed.
3. The devices run the same mutually authenticated TLS 1.3 session they use on the LAN, end to end through the splice. The relay sees only ciphertext, timing, and message sizes.

A room name is a rendezvous secret, not an authentication factor. Someone who learns it can take a slot and block the room, but cannot complete the pinned TLS handshake. Request URLs, and therefore room names, appear in the operator's Workers logs.

Limits: binary messages only, each at most 1 MiB + 64 KiB. Text frames close the room with code 1003, and oversized messages close it with 1009.

## Develop and deploy

```bash
cd relay/worker
npm ci
npm run check     # wrangler types, tsc, and a dry-run bundle
npx wrangler deploy
```

Logs and traces are enabled in `wrangler.json`. The Worker logs one JSON line per event (`join`, `leave`, `rejected`, `error`) and never logs message contents.

## Alternatives

NAT traversal may not need a custom relay at all. QUIC hole punching with a relay fallback (for example, iroh) or Syncthing's public relays solve the same problem without running a service.
