# Roadmap

Synk's goal is document sync a non-technical person can set up in a minute and trust with their only copy: Obsidian, Ulysses, iA Writer, and iWork libraries kept the same on every Mac and iOS device they own, with no account and no service of ours in the way.

The phases come from the [October 2026 assessment](docs/ASSESSMENT.md), which has the findings (C1, H5, and so on) each phase refers to. Each phase has a check that says when it's done. Version 0.1.0 is the first release anyone outside the project should run: the end of Phase 4.

## Principles

- **Documents first.** The safety rules in the README outrank every feature and every deadline.
- **No required service.** Pairing, sync, and the relay all work with nothing but the user's own devices. Anything we host is optional, and a user can run their own from this repository.
- **Honest docs.** The README and docs never claim more than the code and its tests show.

## Phase 0: Stop Data Loss and Close the Open Doors (landed)

Scan gaps, symlinked roots, iCloud stubs, and newly ignored files no longer become deletes; a mass-delete brake holds large deletes for confirmation; peer paths are confined; apply snapshots or aborts; the local API needs a token and rejects browsers.

**Done when** the assessment reproductions pass in `go test ./...`, the mass-delete brake exists, and the README makes no claim the code doesn't meet. Remaining: confirm the macOS-specific fixes (iCloud dataless files, APFS case handling, privacy denials) on a real Mac, and write down what was checked.

## Phase 1: Secure Transport and Pairing (landed)

Peers talk mutual TLS 1.3 with pinned Ed25519 keys under the versioned `synk/2` protocol ([docs/PROTOCOL.md](docs/PROTOCOL.md)), and pairing is bound to the offer.

**Done when** tests with an attacker relaying traffic between two devices show replay, tampering, and man-in-the-middle can neither read nor change anything, pairing pins the QR fingerprint, and library sharing is explicit. Remaining: update [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) to cover the browser, other local users, an on-path attacker, and a compromised paired device.

## Phase 2: Scale and Efficiency (mostly landed)

Changed-file-only scans, chunked transfer of large files, store garbage collection, and vault limits.

**Done when** a 4 GB file syncs and resumes after an interruption, an idle daemon with a 20 GB library reads close to zero bytes a minute, memory use doesn't depend on file size, and `SYNK_DIR` overhead is bounded by the kept versions plus a small fixed share of library size. Each of these needs a benchmark or test in the repository that shows it.

## Phase 3: The Daemon as Single Writer, and Observable

The daemon owns the data folder and the CLI goes through it (landed), with `versions`, `restore`, and `log`. Still to do: `pause`, `resume`, `unpair`, and `remove`; an event stream for the apps; journal compaction; and remaining items H10, M7, and M9.

**Done when** the CLI and the apps never write `SYNK_DIR` directly, and every failure shows up in status with a reason a person can act on.

## Phase 4: Mac App (0.1.0)

The daemon bundled in the app and registered as a login item; onboarding (name the device, pick apps, pair by code or QR, Bonjour discovery); a conflict center and restore view; the iCloud "move" flow; an Xcode project in the repository, built and tested in CI.

**Done when** someone who has never opened a terminal can pair two Macs and sync Obsidian and Keynote, and a signed build can be produced from the repository.

## Phase 5: iOS

Check per app which iOS integration is possible; a public `mobile` package for gomobile; sync into user-picked folders and/or a File Provider extension; background scheduling; QR pairing with the camera.

**Done when** iPhone-to-Mac sync works for iWork and, if feasible, Obsidian, with the docs honest about background limits.

## Phase 6: Beyond the LAN and Beyond Two Devices

NAT traversal with a relay client in the daemon; a self-hostable relay (a `synk-relay` binary as well as the Cloudflare Worker), so no user depends on ours; tested topologies with three or more devices; selective sync; optional encryption of the vault at rest.

**Done when** two devices on different networks sync through a relay the user runs themselves, end to end encrypted, and a three-device test topology passes in CI.
