# Roadmap

Synk's goal is document sync a non-technical person can set up in a minute and trust with their only copy: Obsidian, Ulysses, iA Writer, and iWork libraries kept the same on every Mac and iOS device they own, with no account and no service of ours in the way.

The phases come from the [October 2026 assessment](docs/ASSESSMENT.md), which has the findings (C1, H5, and so on) each phase refers to. Each phase has a check that says when it's done. Version 0.1.0 is the first release anyone outside the project should run: the end of Phase 4. The code through Phase 6 is complete; the remaining work below is real-device validation and signing, which the maintainer performs.

## Principles

- **Documents first.** The safety rules in the README outrank every feature and every deadline.
- **No required service.** Pairing, sync, and the relay all work with nothing but the user's own devices. Anything we host is optional, and a user can run their own from this repository.
- **Honest docs.** The README and docs never claim more than the code and its tests show.

## Phase 0: Stop Data Loss and Close the Open Doors (landed)

Scan gaps, symlinked roots, iCloud stubs, and newly ignored files no longer become deletes; a mass-delete brake holds large deletes for confirmation; peer paths are confined; apply snapshots or aborts; the local API needs a token and rejects browsers.

**Done when** the assessment reproductions pass in `go test ./...`, the mass-delete brake exists, and the README makes no claim the code doesn't meet. The macOS-specific fixes (iCloud dataless files, APFS case handling, privacy denials) were confirmed on a real Mac and written down in [docs/MACOS_CHECKS.md](docs/MACOS_CHECKS.md) (2026-11-16).

## Phase 1: Secure Transport and Pairing (landed)

Peers talk mutual TLS 1.3 with pinned Ed25519 keys under the versioned `synk/2` protocol ([docs/PROTOCOL.md](docs/PROTOCOL.md)), and pairing is bound to the offer.

**Done when** tests with an attacker relaying traffic between two devices show replay, tampering, and man-in-the-middle can neither read nor change anything, pairing pins the QR fingerprint, and library sharing is explicit. [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) covers the browser, other local users, an on-path attacker, and a compromised paired device (rewritten 2026-11-16; extended for vault encryption 2026-11-17).

## Phase 2: Scale and Efficiency (landed)

Changed-file-only scans, chunked transfer of large files, store garbage collection, and vault limits.

**Done when** a 4 GB file syncs and resumes after an interruption, an idle daemon with a 20 GB library reads close to zero bytes a minute, memory use doesn't depend on file size, and `SYNK_DIR` overhead is bounded by the kept versions plus a small fixed share of library size. Each of these needs a benchmark or test in the repository that shows it. Proven 2026-11-16: a 4 GiB file synced in 61.7 s and resumed from the interrupted GiB; a warm scan of 19.5 GB across 200,000 files read zero content bytes (`TestWarmScanReadsNoContent`); a 256 MiB transfer raised peak memory by 1 MiB; `TestSynkDirOverheadIsBounded` bounds the overhead.

## Phase 3: The Daemon as Single Writer, and Observable

The daemon owns the data folder and the CLI goes through it (landed), with `versions`, `restore`, `log`, `pause`, `resume`, `unpair`, and `remove`; an event stream (`GET /v1/events`) serves the apps; journal compaction is in place; H10, M7, and M9 are landed (decision 0005, 2026-11-16).

**Done when** the CLI and the apps never write `SYNK_DIR` directly, and every failure shows up in status with a reason a person can act on.

## Phase 4: Mac App (0.1.0, code complete)

The daemon bundled in the app and registered as a login item; onboarding (name the device, pick apps, pair by code or QR, Bonjour discovery); a conflict center and restore view; the iCloud "move" flow; an Xcode project in the repository, built and tested in CI.

**Done when** someone who has never opened a terminal can pair two Macs and sync Obsidian and Keynote, and a signed build can be produced from the repository. The code side is complete (bundled daemon as login item, three-step onboarding with Bonjour discovery, conflict center and restore view, iCloud move, `make release` signing machinery); the first sentence is the maintainer's validation, and a distributable signature needs the maintainer's Developer ID certificate.

## Phase 5: iOS (code complete)

Check per app which iOS integration is possible; a public `mobile` package for gomobile; sync into user-picked folders and/or a File Provider extension; background scheduling; QR pairing with the camera.

**Done when** iPhone-to-Mac sync works for iWork and, if feasible, Obsidian, with the docs honest about background limits. Code complete 2026-11-17: the gomobile `mobile` package (decisions 0008/0009), libraries via folder picking in the app sandbox (decision 0010), Files → On My iPhone → Synk via `UIFileSharingEnabled`, a read-only File Provider extension (decision 0011), background scheduling (decision 0014), and camera QR pairing. Simulator tests pair and sync both ways (`make ios-test`, a CI step); the done-when's real-device half is the maintainer's validation.

## Phase 6: Beyond the LAN and Beyond Two Devices (code complete)

NAT traversal with a relay client in the daemon; a self-hostable relay (a `synk-relay` binary as well as the Cloudflare Worker), so no user depends on ours; tested topologies with three or more devices; selective sync; optional encryption of the vault at rest.

**Done when** two devices on different networks sync through a relay the user runs themselves, end to end encrypted, and a three-device test topology passes in CI. Code complete 2026-11-17: the Go relay and its client (decisions 0012/0013 — the secret derives from the peers' keys, rooms include the library), relay fallback dialing, `synk relay` configuration, `bin/synk-relay` from `make build`, the Cloudflare Worker, selective sync (decision 0015), and vault encryption at rest (decision 0016). The three-device relay topology passes in CI; the two-network run is the maintainer's validation.
