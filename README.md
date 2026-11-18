# Synk

**Keep writing, not wiring.**

Synk is a document sync system for people who want Obsidian, Ulysses, iA Writer, Keynote, Pages, and Numbers on every Mac and iOS device they own — without iCloud, without rsync recipes, and without a computer-science hobby.

Install. Name this device. Click the apps to keep in sync. Scan the other device. That is the whole configuration.

## Why this exists

Apple’s sync is proprietary, opaque, and hostile to the documents it claims to protect. iWork files are packages; iCloud placeholders look like files; a “delete on one machine” can evaporate a vault before anyone notices. Technical users survive with Syncthing. Everyone else lives with drift, duplicates, and dread.

Synk is the product in the middle: Syncthing-grade caution, none of the control panel.

## What it does

- **App-aware.** Presets for Obsidian, Ulysses (local library), iA Writer, Keynote, Pages, Numbers, Logseq, Scrivener, and any folder you point at.
- **Package-safe.** `.pages`, `.key`, `.numbers`, `.textbundle`, `.scriv` are copied as one unit. Interior files are never applied independently.
- **Paranoid.** Version vectors, not clocks. Every overwrite is snapshotted. Concurrent edits keep **both** versions. Concurrent deletes lose to edits. iCloud placeholders are never treated as deletes. Bytes that do not match their hash go to quarantine.
- **Paired, not public.** Devices trust each other only after a code/QR exchange. There is no required account.
- **Honest about iOS.** The companion app syncs while it is open (and during the background time iOS actually grants). Open a library from Files → Synk. A Mac on the same pair is the usual always-on peer.

## License note (Syncthing)

Syncthing is MPL-2.0. That license **does** allow wrapping it in a larger application so long as Syncthing files stay MPL and their source remains available.

The engine in this repository is original (Apache-2.0) because iWork packages, iOS File Provider, and iCloud placeholders need a safety layer Syncthing does not provide. Syncthing can still be an optional bulk transport later; see [docs/LICENSE_AND_SYNCTHING.md](docs/LICENSE_AND_SYNCTHING.md).

## Status

Synk is pre-release. The engine passes its test suite, including reproductions of every critical finding in the [October 2026 assessment](docs/ASSESSMENT.md), but it hasn't been tested on real hardware with real libraries yet. Don't point it at documents you can't afford to lose, and keep your own backups.

| Piece | State |
| --- | --- |
| Sync engine, vault, journal, reconcile | Tested; assessment safety fixes landed |
| Pairing and peer transport | Tested: mutual TLS 1.3 with pinned Ed25519 keys ([protocol](docs/PROTOCOL.md)) |
| App catalog and discovery | Present |
| Folder watch and peer catch-up | Present |
| `synk` CLI and loopback API | Present; the API needs a bearer token and rejects browsers |
| Kept versions and restore | Present (`synk versions`, `synk restore`) |
| macOS SwiftUI app | Built and tested in CI against two real daemons; release signing machinery ready |
| iOS SwiftUI app | Builds for the simulator with the engine embedded; pairing, libraries, camera QR pairing and the read-only File Provider extension tested there |
| Optional NAT relay | Self-hostable `synk-relay` binary with a Go client in the daemon (`synk relay wss://HOST` sets it); a Cloudflare Worker in `relay/worker` is the optional hosted convenience |
| Selective sync | Present: per-device ignore patterns per library (`synk ignore`), never propagated as deletions ([decision 0015](docs/decisions/0015-selective-sync-is-per-device-ignore-patterns.md)) |
| iOS background sync | Best effort: a flush when the app is backgrounded, then BGTaskScheduler refresh/processing tasks at the system's discretion; no push notifications, which would require a service of ours ([decision 0014](docs/decisions/0014-ios-background-sync-is-opportunistic.md)) |

What comes next is in [ROADMAP.md](ROADMAP.md), and where the work stands is in [STATUS.md](STATUS.md).

## Quick start (engine)

```bash
git clone https://github.com/bristlecone-zSuite/zSynk
cd zSynk
make test
make build

# Device A
export SYNK_DIR=/tmp/synk-a
./bin/synk init --name "Studio Mac"
./bin/synk enable --preset obsidian --path ~/Notes
./bin/synk serve

# Device B (other terminal)
export SYNK_DIR=/tmp/synk-b
./bin/synk init --name "iPhone"
./bin/synk enable --preset obsidian --path /tmp/notes-b
# pair, then:
./bin/synk sync --addr 127.0.0.1:7420 --folder obsidian
```

The Mac and iOS apps talk to the same daemon over `http://127.0.0.1:7421` (loopback only).

## Using the relay

When two devices have no working direct connection (NAT, different networks),
run your own relay from this repository and point both devices at it:

```bash
make build && ./bin/synk-relay -addr :7430 -cert CERT.pem -key KEY.pem
# on each device:
./bin/synk relay wss://relay.example.com
```

The relay forwards encrypted traffic only: it sees room names, never keys or
files. `synk relay` with no argument shows what is set; `synk relay clear`
removes it. See [docs/PROTOCOL.md](docs/PROTOCOL.md) for the wire protocol.

## Selective sync

A device can leave a subtree alone while the others sync it in full. The
patterns are per device — they never travel the wire, and they never turn
into deletions: ignoring a path that already synced keeps the local copy,
and a peer's delete of an ignored path does not delete it here. Last
matching pattern wins, so `!` re-includes; an ignored directory is not
scanned into, so a re-include cannot rescue paths under an excluded
directory. See [decision 0015](docs/decisions/0015-selective-sync-is-per-device-ignore-patterns.md).

```bash
./bin/synk ignore --library obsidian                      # list
./bin/synk ignore --library obsidian --add 'Archive/**'   # add (repeatable)
./bin/synk ignore --library obsidian --remove 'Archive/**'
./bin/synk ignore --library obsidian --clear
```

## Safety rules (non-negotiable)

1. Never propagate a delete from an incomplete scan.
2. Never write bytes whose hash does not match.
3. Never apply a package until the whole tree verifies.
4. Snapshot before overwrite.
5. Conflicts are extra files, not dropped sides.

## Docs

- [Architecture](docs/ARCHITECTURE.md)
- [Threat model](docs/THREAT_MODEL.md)
- [App presets](docs/APPS.md)
- [Xcode / companion apps](docs/XCODE.md)
- [Peer protocol](docs/PROTOCOL.md)
- [Assessment (October 2026)](docs/ASSESSMENT.md)
- [Roadmap](ROADMAP.md)

## Contributing

Issues and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first. Report security problems privately, as [SECURITY.md](SECURITY.md) describes, never in a public issue.

## License

Apache-2.0. Copyright 2026 Corey J. Mahler. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
