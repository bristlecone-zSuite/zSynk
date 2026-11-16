# Synk repository assessment

Assessed: `main` at `dfaf1b8`, October 2026.
Scope: Go engine and CLI, loopback API, Swift macOS and iOS sources, Cloudflare relay, docs, tests, and CI.

## Summary

Synk is aimed at the right problem, and its core design is sound. It uses version vectors instead of clocks, content addressing with hash checks, a journal around every write, packages applied as one unit, and a rule that a concurrent edit beats a delete. The engine is small (about 4,300 lines of Go in 28 files, with no third-party dependencies) and easy to read. It has unit tests, and `go test`, `go vet`, and `gofmt` are all clean.

It is not yet safe to run on documents that matter. The assessment found 5 critical, 12 high, 13 medium, and 8 low-severity issues. Twenty-one failing tests on this branch reproduce 16 of them, including every critical one (run `make assess`). The critical issues fall into two groups:

- **Ordinary filesystem events delete files on other devices.** Several things that are not deletions get treated as deletions and sent to every peer: a folder the scanner can't read, a library path that is a symlink, a file iCloud has evicted, or a file a newer preset ignores. This breaks README safety rule 1 and the iCloud promise.
- **The network and the local API are open.** Peer messages are unencrypted, and only their headers are signed. Anyone on the same Wi-Fi can read synced documents, replay a captured session to download a library, or change what gets applied. Paths sent by a peer aren't confined, so a peer can write anywhere in the user's home folder. So can anyone who can inject into a session. Any web page or other local user can drive the loopback API, which is enough to pair the daemon with an attacker's machine.

Beyond safety, the product claims are ahead of the code:

- The iOS app is a static mock.
- The Mac app can't decode a fresh daemon's status, and it never launches the daemon.
- The relay can't be deployed, and the daemon has no relay client.
- Files over 32 MiB never sync.
- Two idle, paired daemons re-hash every byte 10 times per 5-second tick.
- `SYNK_DIR` keeps a full copy of every library and every version, forever.

**Recommendation.** Pause feature work and run a safety phase (Phase 0) whose goal is to make the failing reproductions pass:

- Confine paths.
- Make scan gaps non-destructive and add a mass-delete brake.
- Lock down the API.
- Make snapshots mandatory and fix crash recovery.

Next, replace the wire protocol with mutually authenticated TLS 1.3 using the Ed25519 keys devices already have (Phase 1), before any build leaves a test machine. After that, work on scale, then the apps. Keep Synk's own engine rather than switching to Syncthing, because the features that set Synk apart live in scan and apply. But stop hand-rolling transport security.

## Scorecard

| Area | State | Findings |
| --- | --- | --- |
| Version vectors, reconcile, conflict naming | Sound design; minor UX issue | M12 |
| Scan and delete safety | Unsafe | C1, C2, H4 |
| Apply, journal, vault | Needs work | C3, H1, H2, H3, M2, M3 |
| Peer transport and pairing | Unsafe | C4, H8, M8 |
| Local API | Unsafe | C5, M7 |
| Performance and disk use | Not viable beyond small libraries | H5, H6, H7 |
| Concurrency and process model | Needs work | H9, H10 |
| macOS app | Not functional | H11, H12 |
| iOS app | UI mock | H12 |
| Relay | Not deployable, not wired in | M10 |
| Docs | Ahead of the implementation | see "Docs vs. reality" |
| Tests and CI | Good unit base; thin integration; minimal CI | M13, L6 |

## How this was assessed

- I read every source file: Go, Swift, the TypeScript Worker, the Wrangler config, the Makefile, the CI workflow, and all docs.
- I ran `go test ./...`, `go vet ./...`, `gofmt -l .`, statement coverage, and the race detector.
- I wrote 21 reproduction tests in `internal/{engine,apply,api,apps}/assessment_test.go`, behind the `assessment` build tag:
  - Each test asserts something the README or docs promise, and all 21 fail on `main` today.
  - The build tag keeps them out of `go test ./...` and CI, so `main` stays green.
  - When a fix lands, its test should pass; then drop the tag so it joins the normal suite.
  - `make assess` runs them with the race detector. The index is at the end of this document.
- For the relay, I ran `wrangler deploy --dry-run` (Wrangler 4.148) on the committed config and on a corrected one, and checked Cloudflare's docs on the Durable Objects storage backend.
- Limits of this assessment:
  - It ran on a Linux VM with no Xcode, so the Swift code wasn't compiled.
  - macOS-specific behavior (APFS case-insensitivity, iCloud dataless files, FSEvents, TCC, the sandbox) comes from reading the code and from platform knowledge. Each spot is marked where it needs checking on real hardware.
  - Network attacks were reproduced over loopback, not a real LAN.

Severity levels:

- **Critical**: loses user data, or gives an outsider read or write access to files, in normal use.
- **High**: loses data under plausible conditions, blocks a core feature, or weakens a security boundary.
- **Medium**: a correctness, robustness, or security gap with narrower impact or a workaround.
- **Low**: hygiene, docs, or maintainability.

## Critical findings

### C1. Gaps in a scan become deletes on every peer

**Where.** `internal/reconcile/advance.go:33-47` tombstones every previously live path that a scan didn't report. `internal/scan/scan.go:26-37` leaves paths out of a scan when they aren't actually gone.

**What happens.** The scanner reports absence in several situations that aren't deletions:

- **Unreadable directory.** `filepath.WalkDir` reports the read error. The callback records it and returns `nil`, so the directory's contents silently disappear from the scan. Permission changes, macOS privacy (TCC) denials for Documents or Desktop, and I/O errors on external disks all trigger this.
- **Symlinked library root.** `os.Stat` follows the link, so the root passes the "is a directory" check. `filepath.WalkDir` doesn't descend into a symlinked root, so the scan returns zero items, and every file in the library is tombstoned.
- **Newly ignored path.** Suppose a preset's ignore list changes, either in a new release or because two devices run different releases. A path that used to sync is now hidden, so it gets tombstoned and deleted on peers.
- **Temporarily empty library folder.** An unmounted volume whose mount point remains, or a folder replaced by another tool, looks exactly like "the user deleted everything."

Peers then delete their copies. They take a vault snapshot first, if the snapshot succeeds (see H1).

**Proof.** These three tests fail: `TestAssessUnreadableDirDoesNotPropagateDeletes`, `TestAssessSymlinkedLibraryRootDoesNotPropagateDeletes`, and `TestAssessNewlyIgnoredFileIsNotDeletedOnPeers`.

**Fix.**

1. Only treat absence as a delete inside directories that were fully read. The scan should return the directories it couldn't read, and `Advance` should keep every previous entry under them, marked `Incomplete`.
2. Resolve the root with `filepath.EvalSymlinks` before walking, or refuse symlinked roots.
3. Treat "now ignored" as a local state: stop announcing the path, but don't tombstone it. Syncthing does this by marking ignored files invalid rather than deleted.
4. Require a marker in each library root (a `.synk/` directory, which the scanner already skips), and refuse to scan when it's missing. This is how Syncthing's `.stfolder` protects against unmounted and replaced folders.
5. Add a mass-delete brake. If one scan would tombstone more than a small threshold (for example 50 files or 10% of the library), pause the library and ask the user.

### C2. iCloud eviction is propagated as a delete

**Where.** `internal/scan/scan.go:49-57` and `internal/packages/packages.go:60-63`.

**What happens.** iCloud replaces an evicted `notes/todo.md` with a stub named `notes/.todo.md.icloud`. The scanner strips only the `.icloud` suffix, so it records an `Incomplete` entry for `notes/.todo.md`, a path that never existed. Meanwhile `notes/todo.md` is missing from the scan, so the C1 rule tombstones it and every peer deletes its copy. This is the exact failure the README says Synk prevents.

There's a related risk on current macOS. Since macOS 14, iCloud Drive is built on File Provider, and it represents evicted files as "dataless" files under their real names rather than as `.icloud` stubs. Reading a dataless file triggers a download, or fails when offline. So the stub check alone isn't enough. This should be confirmed on a current Mac.

**Proof.** `TestAssessICloudEvictionIsNotADelete` fails.

**Fix.**

1. Map `dir/.name.icloud` to `dir/name`.
2. Detect dataless files with `st_flags & SF_DATALESS` and mark them `Incomplete` without reading them.
3. Refuse to enable libraries under `~/Library/Mobile Documents` or `~/Library/CloudStorage` (see M1).

### C3. Paths sent by a peer escape the library and the vault

**Where.** Paths and names from peers are joined onto local directories without validation in these places:

- `internal/apply/apply.go:129` and `:156`: `filepath.Join(e.Root, filepath.FromSlash(op.Path))`.
- `internal/packages/packages.go:146`: the `RelPath` values in a package manifest.
- `internal/vault/vault.go:44` and `:70`: snapshot paths built from the same relative path.
- `internal/vault/vault.go:92` together with `internal/engine/session.go:194-196`: the quarantine directory is named after the hash the peer claims.

**What happens.** Index entries, package manifests, and blob headers all come from the peer. `../` sequences let a peer create, overwrite, or delete any file the user can write. So do symlinks inside the library, which the scanner skips but `filepath.Join` follows. For example, a peer could write a LaunchAgent plist that runs at the next login.

The threat model puts "a malicious paired device" out of scope. But because of C4 and C5, the attacker doesn't need to be a paired device. And even a compromised paired device should be limited to the libraries it shares.

**Proof.** Two tests fail:

- `TestAssessRemoteIndexCannotEscapeLibraryRoot` writes `../escaped.txt` next to the library.
- `TestAssessPeerBlobHashCannotEscapeQuarantine` sends a blob frame whose hash field is a path, and the peer's bytes are written outside `SYNK_DIR`.

**Fix.**

1. Validate once, where data enters from the protocol:
   - Every relative path must pass `filepath.IsLocal`, be slash-separated and NFC-normalized, and not point inside `.synk/`.
   - Every hash must pass `blob.ValidateHash` before it's used.
2. Do every apply-side file operation through `os.Root`. It was added in Go 1.24, and Go 1.25 added `Rename`, `MkdirAll`, and `RemoveAll`. It refuses `..` and symlink escapes at the system-call level, which string checks can't do.

### C4. The peer protocol can be read, replayed, and tampered with

**Where.**

- `internal/transport/frame.go:119-126`: plain TCP.
- `internal/engine/auth.go:21,42` and `internal/identity/identity.go:192-194`: the signature covers only `kind|deviceId|nonce|folderId|unix`.
- `internal/identity/identity.go:189,204-216`: a ±5-minute timestamp window, and nonces are never recorded.
- `internal/engine/session.go:164-171`: `serveWants` never verifies the `want` frame.
- Blob frames are never signed at all.

**What happens.**

- **Confidentiality.** Everything crosses the network in cleartext: file contents, indexes, and the pairing token. Anyone on the same Wi-Fi can read every synced document.
- **Replay.** The same device accepts a captured `hello` again for up to five minutes, or longer with clock skew. Whoever replays it receives the library index. Because `want` isn't checked, they then receive any blob they ask for.
- **Tampering.** The signature doesn't cover the `index` payload. An attacker on the network path can rewrite it to inject overwrites or deletes, and, with C3, writes outside the library.

**Proof.** Two tests fail:

- `TestAssessIndexSignatureCoversPayload`: an index payload changed after signing still verifies.
- `TestAssessReplayedHelloCannotReadFiles`: a replayed hello downloads `diary.md` without B's private key.

**Fix.** Don't patch the per-frame signatures; replace the channel.

- Use TLS 1.3 with mutual authentication. Each device presents a self-signed certificate for the Ed25519 key it already has, and each side pins the peer's public key from the pairing record. `crypto/tls` supports Ed25519 certificates, and Syncthing uses the same pinned-certificate model.
- Alternatively, use Noise IK or XX.

Once the channel authenticates both ends and protects every byte, the per-frame signatures, timestamps, and nonces can go. Put an explicit protocol version in the first frame. A configuration sketch is in "Concrete changes."

### C5. Any web page or local user can drive the local API

**Where.** `internal/api/server.go:224-239`. The only check is that the TCP peer is a loopback address. There is no Host or Origin check, no credential, and no content-type check, and the handlers decode any request body as JSON.

**What happens.** Browsers are loopback clients. A page can send a "simple" cross-origin POST (`Content-Type: text/plain`, so no preflight) whose body is JSON. The handler runs it, even though the page can't read the response. With DNS rebinding, the page can read responses too.

The browser isn't the only way in. Other OS user accounts on the same Mac can reach the port, and so can any sandboxed app with network access. Recent Chrome versions ask permission before a public site can reach localhost, but other browsers vary, and the API shouldn't depend on browser policy anyway.

A concrete attack:

1. The page sends `POST /v1/pair/join` with the attacker's address, code, and token. The daemon dials out, pairs, and trusts the attacker's key, because the joining side accepts whatever key answers (H8).
2. The 5-second loop now syncs every enabled library with the attacker, because `ShareWith` is ignored (M6). Library IDs are easy to guess: the CLI uses the preset name, such as `obsidian`.
3. Alternatively, the page sends `POST /v1/libraries` with `{"id":"x","preset":"custom","path":"/Users/<name>"}` to share the whole home folder.
4. The attacker reads anything it asks for with `want`, and writes files outside the libraries through C3.

**Proof.** Two tests fail:

- `TestAssessCrossOriginPostRejected`: a `text/plain` POST from `https://evil.example` enables a library.
- `TestAssessForeignHostRejected`: a request with Host `rebind.evil.example` reads the live pairing code and token.

**Fix.**

1. Require a credential. Either use a per-install bearer token stored with mode 0600 in `SYNK_DIR` (the Mac app reads it), or move the API to a Unix domain socket in `SYNK_DIR` and check the connecting process's user ID with `getpeereid`.
2. In addition:
   - Reject requests whose Host isn't `127.0.0.1:<port>` or `localhost:<port>`.
   - Reject any request that carries an Origin header.
   - Require `application/json` on POST.
   - Enforce the HTTP method on every route.

## High findings

### H1. "Snapshot before overwrite" is best-effort

**Where.** `internal/apply/apply.go:134-140` (delete) and `:190-200` (overwrite) discard snapshot errors.

**What happens.** Suppose the vault write fails. Possible causes:

- The disk is full. The free-space check counts only incoming bytes, not the snapshot.
- Permissions.
- A path that's too long.
- An I/O error.

The overwrite or delete goes ahead anyway, and the old bytes are gone. This breaks README rule 4.

**Proof.** `TestAssessOverwriteAbortsWhenSnapshotFails` fails.

**Fix.** A failed snapshot should abort the operation (journal status `aborted`) and report an error. Include the snapshot's size in the free-space check. Better still, make snapshots references into the blob store (H7), which makes them nearly free and rarely failing.

### H2. Crash recovery writes one library's data into another

**Where.** A device has a single journal (`internal/engine/engine.go:63`). But `Env.Recover` (`internal/apply/apply.go:33-55`) replays every incomplete record, and `finishPrepared` (`:57-69`) joins the record's path onto the root of whichever library is being pulled.

**What happens.** Say the daemon dies in the middle of applying a change to library A. On the next pull of library B, recovery moves A's staged bytes onto B's file with the same relative path, without taking a snapshot. It then marks the record committed, so A never gets its write either. Deletes are replayed the same way, with `os.RemoveAll` on B's path.

**Proof.** `TestAssessRecoverOnlyTouchesItsOwnLibrary` fails.

**Fix.**

- Skip records whose `FolderID` doesn't match the library being recovered.
- Run recovery once per library at startup, before the watcher starts.
- Snapshot before finishing a recovered write, and re-verify the staged content's hash.
- Keep staging inside the library (M2), so a record can't point at another tree.

### H3. Apply overwrites edits made after the scan

**Where.** `Node.PullFrom` scans, then fetches blobs (which can take minutes on a slow link), then `applySet` renames over the destination without checking it again (`internal/apply/apply.go:190-226`).

**What happens.** A save that lands between the scan and the rename is replaced, and no conflict copy is made. The user's bytes survive only in the vault, if H1 didn't strike, and the vault has no restore UI (M3).

**Proof.** `TestAssessApplyRefusesToOverwriteUnscannedEdit` fails.

**Fix.** Just before replacing a file, compare it to what the index says: check size, mtime, and inode first, and the hash when those are ambiguous. If the file changed, skip the operation and rescan; the next round then produces a proper conflict. Syncthing makes the same check.

### H4. Packages can be captured in the middle of a save

**Where.** `internal/scan/scan.go:70-78` hashes whatever a package contains at that instant. `Node.Busy` (`internal/engine/engine.go:33`) is declared but never used.

**What happens.** Keynote, Pages, and Scrivener save a package as several files written over a period of time. If a scan runs mid-save, Synk records a torn package as a valid version and delivers it, atomically, to every peer. That's likely with a 5-second rescan loop (H6). Atomic apply only guarantees that the peer receives exactly the torn state.

The Keynote preset says "Synk waits until the package is idle." Nothing implements that.

**Fix.** Require the package to be quiet before announcing a new version:

- The newest mtime inside the package must be older than a settle window, for example 10 seconds.
- A second manifest, taken after the window, must match the first.

On macOS, read packages under `NSFileCoordinator` (from the Swift side, or through a small cgo shim), so the app's own save finishes first.

### H5. Files over 32 MiB never sync, and one blocks its whole library

**Where.**

- `internal/transport/frame.go:12` sets `MaxFrame` to 32 MiB, and `internal/engine/session.go:172-180` sends each blob as one frame.
- `internal/blob/store.go:91-98`, `internal/atomicfile/write.go:121-139`, and `internal/packages/packages.go:66-72` read whole files into memory.

**What happens.** The sender refuses to write a frame over 32 MiB, and the session dies with EOF. Nothing else in that library syncs in that round, or in any later round. Keynote decks with video, Scrivener research folders, and PDFs routinely exceed 32 MiB. Separately, every scan reads each file entirely into RAM.

**Proof.** `TestAssessLargeFileSyncsOverNetwork` fails: a file of 32 MiB plus one byte stops `small.md` from arriving.

**Fix.** Split files into blocks (Syncthing uses 128 KiB to 16 MiB depending on file size). Hash in a single streaming pass, store and transfer blocks, and reassemble them in staging. This also gives resumable transfers and makes small edits to big files cheap.

### H6. Idle daemons keep re-reading and re-hashing everything

**Where.**

- `internal/engine/loop.go:51-63` rescans every library every 5 seconds, whether or not the watcher saw a change.
- `ExportIndex` and `PullFrom` (`internal/engine/engine.go:181-193` and `:229-266`) each rescan again, on both ends of every session.
- `internal/scan/scan.go:82` has no size/mtime cache.

**What happens.** I measured two paired, idle daemons. They read 80 MiB in 6.5 seconds for one unchanged 8 MiB file, which is 10 full re-hashes per tick. At that rate, a 20 GB library means about 200 GB of reads every 5 seconds. A tick never finishes, the CPU and disk never rest, and laptops drain their batteries.

**Proof.** `TestAssessIdleDaemonDoesNotRehashUnchangedFiles` fails.

**Fix.**

- Store size, mtime, inode, and ctime for each entry, and re-hash a file only when one of them changes.
- Replace the 2-second polling with FSEvents on macOS and inotify on Linux.
- Scan once per batch of changes, not once per session.
- Exchange index changes by sequence number instead of sending full indexes.

### H7. `SYNK_DIR` duplicates every library and never frees space

**Where.**

- The scanner stores every file in the blob store (`internal/scan/scan.go:82`, `internal/blob/store.go:57-70`), and nothing ever deletes a blob.
- The vault makes its own full copies (`internal/vault/vault.go:42-66`) and keeps 20 per path regardless of size (`:14`, `:113-128`).

**What happens.** Enabling a 50 GB library immediately uses another 50 GB in `SYNK_DIR`, and every saved version adds another full copy, forever. On a laptop with a large iWork or media library, this fills the disk. The free-space check doesn't count growth in `SYNK_DIR`.

**Fix.**

- Add garbage collection for the blob store: keep only the blocks referenced by current indexes and retained vault versions.
- Make vault entries manifests that reference blobs, instead of copies.
- Retain versions by size and age.
- Consider not copying files into the store at all. The index could hold only hashes, and blocks could be read from the library when they're needed for a transfer, checking the hash at read time.

### H8. Pairing isn't tied to the device you meant to pair with

**Where.**

- `internal/engine/session.go:231` verifies the pairing reply using the public key carried in that same reply.
- The QR code's `fp=` value (`internal/api/server.go:129`, `cmd/synk/main.go:230`) is displayed but never checked.
- The code and token cross the network in cleartext (C4).

**What happens.** An attacker on the network path during pairing can do both of these:

- Answer the joining device with its own key.
- Present the joiner's token to the real offering device, also with its own key.

Both devices end up trusting the attacker. The fingerprint is also only 64 bits, and it's computed over the base32 string rather than the raw key.

**Fix.**

- Put the offering device's full key fingerprint (at least 128 bits) and its addresses in the QR code.
- The joining device pins that fingerprint as the TLS peer key (C4) and refuses any other key.
- For typed codes, use a password-authenticated key exchange (SPAKE2 or CPace), so a short code is safe against interception. Show a short confirmation string on both screens.

### H9. Data races in the daemon

**Where.**

- `JoinPeer` writes `Config.Peers` without holding the lock (`internal/engine/session.go:234-241`) while the sync loop reads it.
- The API's `GET /v1/libraries` reads `Config.Libraries` without the lock (`internal/api/server.go:79`).
- `Status()` returns the internal `Peers` slice, which is JSON-encoded after the lock is released (`internal/engine/engine.go:394`).

**What happens.** Pairing and syncing at the same time can corrupt the peer list or crash the daemon. CI never runs the race detector, so none of this shows up.

**Proof.** `TestAssessDaemonLoopRaceFree` fails; the race detector reports 3 races.

**Fix.** Route all config reads and writes through `Node` methods that hold `n.mu`, and return copies. Add `-race` to CI.

### H10. CLI commands and the daemon overwrite each other

**Where.** Every subcommand opens the same data directory (`cmd/synk/main.go:93-98`). `serve` keeps its own in-memory config and rewrites `config.json` wholesale (`internal/config/config.go:68-78`). There's no lock, and `serve` never reloads.

**What happens.** Running `synk enable` while `serve` is running appears to work. But the daemon never sees the new library, and it drops it the next time it saves config, for example after pairing. Index and journal files are also written by two processes at once.

**Proof.** `TestAssessCLIChangesSurviveRunningDaemon` fails.

**Fix.**

- The daemon takes an exclusive lock on `SYNK_DIR`.
- While a daemon is running, CLI commands go through the authenticated API (C5).
- Config writes become read-modify-write operations under the lock.

### H11. Libraries enabled from the app never sync between devices

**Where.** `internal/api/server.go:86-88` assigns IDs of the form `<preset>-<timestamp>`, and sessions match libraries by ID (`internal/engine/session.go:102-108`). The CLI uses the preset name instead (`cmd/synk/main.go:182-184`).

**What happens.** Enabling Obsidian from the app on two Macs produces the IDs `obsidian-20261007143314` and `obsidian-20261007143315`. The two libraries never exchange a byte, and the error goes into a discarded return value (M9).

**Proof.** `TestAssessAppEnabledLibrariesShareAnID` fails.

**Fix.** In the short term, use the preset ID, as the CLI does. In the long term, make sharing explicit. Give a library a random ID once, and have the other device ask "Studio Mac shares Obsidian. Accept?", the way Syncthing offers folders.

### H12. The Mac app fails on first launch, and the iOS app is a mock

**Where.**

- `internal/engine/engine.go:394-405`: `Status` encodes empty `libraries` and `peers` as `null`.
- `apps/shared/SynkClient.swift:5-10`: decodes them as non-optional arrays.
- `apps/shared/SynkClient.swift:72-106`: ignores the HTTP status of every request.
- `apps/macos/Synk/SynkApp.swift:39-42`: the "Launching the local engine" message.
- `apps/ios/Synk/SynkApp.swift:33-36,114`: the pairing screen.
- `apps/ios/Synk/FileProvider/Enumerator.swift:16-18`: the File Provider enumerator.

**What happens.**

- **Mac.** On a fresh daemon the status decode throws. The app then says "Synk is not running yet. Launching the local engine…" while the daemon is in fact running, and nothing ever launches the engine. Failed POSTs (HTTP 400) look like success. There's no Xcode project, no UI for joining a pairing, and no conflict or restore UI.
- **iOS.** There's no engine and no networking. "Pair" only advances to the next page, the File Provider lists nothing, and app selections aren't saved. The README and `ARCHITECTURE.md` describe all of this as working.

**Proof.** `TestAssessStatusEncodesEmptyArrays` fails.

**Fix.**

- Initialize the slices so the JSON contains `[]`, and decode with defaults on the Swift side.
- Check `HTTPURLResponse.statusCode` on every request.
- Ship the daemon inside the app and register it with `SMAppService` (macOS 13 and later).
- Commit the Xcode project (or a Swift package plus an XcodeGen spec) so CI can build it.

For iOS, see Phase 5 of the roadmap.

## Medium findings

### M1. The default Obsidian path is the iCloud container

**Where.** `internal/apps/catalog.go:56-60` lists `~/Library/Mobile Documents/iCloud~md~obsidian/Documents` first, and `synk enable` takes `Paths[0]` (`cmd/synk/main.go:194-200`). The iA Writer preset lists its iCloud container too.

**What happens.** By default, the library goes inside iCloud Drive. The preset's own warning says not to do that, and C2 makes it dangerous. `APPS.md` promises that Synk refuses iCloud vaults and offers to move them, but no such check exists.

**Proof.** `TestAssessObsidianDefaultIsNotICloud` fails.

**Fix.** Never auto-select a path under `~/Library/Mobile Documents` or `~/Library/CloudStorage`. Refuse to enable those paths without an explicit override, and build the "move to a Synk folder" flow in the app.

### M2. Packages can't be applied across volumes

**Where.** Staging lives in `SYNK_DIR` (`internal/engine/engine.go:259`), and `atomicfile.ReplaceDir` uses `os.Rename` (`internal/atomicfile/write.go:97`). Single files fall back to copying (`internal/apply/apply.go:217-225`); packages don't.

**What happens.** If a library is on an external drive or another APFS volume, every package fails with "invalid cross-device link," on every round.

**Proof.** `TestAssessPackageAppliesAcrossVolumes` fails.

**Fix.** Stage inside the library, in `<library>/.synk/staging`, which the scanner already skips. Then the final rename is always on one volume, and atomic.

### M3. The vault can't restore anything, and has no retention policy

**Where.** `internal/vault/vault.go`. There's no CLI command, API route, or UI for listing or restoring versions, and quarantine is never pruned.

**What happens.** "Every overwrite is snapshotted" is the safety net users are promised. But recovering a file means browsing `SYNK_DIR/vault/versions/<library>/<path>/<timestamp>/content` by hand. Retention is a fixed 20 copies per path, regardless of size or age.

**Fix.**

- Add `synk versions <path>` and `synk restore <path> [--at TIME]`, plus a "Recently replaced" view in the app.
- Retain versions by size and age; Syncthing's staggered versioning is a good model.
- Store versions as blob references (H7).

### M4. File metadata is dropped

**Where.** Every received file is written with mode 0600 (`internal/apply/apply.go:185`, `internal/packages/packages.go:147`). The index has no mode, mtime, or extended attributes.

**What happens.** Executable bits, modification times, Finder tags and other extended attributes, and creation dates don't sync. Apps that sort by modification time, which includes most note apps, show every synced file as "just modified" on the receiving device.

**Fix.** Carry the mode (at least the executable bit), the mtime, and an allowlist of extended attributes in each entry, and apply them after the rename. An example for the allowlist is Finder tags (`com.apple.metadata:_kMDItemUserTags`).

### M5. Case and Unicode collisions on APFS aren't handled

**Where.** Index keys are raw strings (`internal/reconcile/reconcile.go:34-38`). Nothing normalizes them or detects collisions.

**What happens.** macOS volumes are case-insensitive and normalization-insensitive by default. A peer can have both `Note.md` and `note.md`, or an `é` in both its NFC and NFD forms. Those produce two index entries that are the same file on a Mac, so the two overwrite each other on every round. This is from analysis only, because this VM's filesystem is case-sensitive.

**Fix.** Normalize keys to NFC. Detect case-fold collisions during reconcile and resolve them with conflict names. Test on a case-insensitive volume in macOS CI.

### M6. `ShareWith` is ignored, so every paired device gets every library

**Where.** `internal/config/config.go:30` declares `ShareWith`, but nothing reads it. `handleHello` serves any enabled library to any paired peer (`internal/engine/session.go:102-108`).

**Fix.** Enforce each library's peer list on both sides, when serving and when pulling. Populate it through the explicit share acceptance described in H11.

### M7. API inputs aren't validated

**Where.** `internal/api/server.go:80-101`.

**What happens.**

- The library ID becomes part of file paths (`indexes/<id>.json`, `staging/<id>`, `vault/versions/<id>`), so `../` in an ID escapes `SYNK_DIR`.
- Any path is accepted, including `/` and the home folder, even though the `bbedit` preset promises that Synk won't sync a home directory.

**Fix.** IDs must match `^[a-z0-9-]{1,64}$`. Paths must be existing directories that are:

- not `/`,
- not the home folder itself,
- not inside `SYNK_DIR`,
- not inside another library.

### M8. Unauthenticated connections can tie up memory and connections

**Where.**

- The daemon listens on `0.0.0.0:7420` by default (`internal/config/config.go:51`).
- `readFrame` allocates whatever length the frame claims, up to 32 MiB, before any authentication (`internal/transport/frame.go:103-117`).
- Each connection gets a 5-minute deadline and its own goroutine, with no limit on connections (`internal/engine/session.go:23-39`).

**What happens.** Anyone on any network the Mac joins can hold hundreds of connections open, each with a large buffer allocated before authentication.

**Fix.**

- Cap frames received before authentication; 64 KiB is plenty.
- Shorten the pre-authentication deadline to a few seconds.
- Limit concurrent connections per IP address.
- Listen only on private LAN interfaces by default.

### M9. Failures are invisible

**Where.**

- `internal/engine/loop.go:41-42,60-62` discards scan and sync errors.
- `scan.Result.Errors`, `Skipped`, and `Placeholders` are collected but never shown anywhere.
- `LibraryState.Placeholders` and `Peer.LastSeen` are never filled in.
- The daemon doesn't log anything.

**What happens.** From the outside, each of these looks exactly like "in sync":

- A library stuck on a 33 MiB file (H5).
- Library IDs that don't match (H11).
- A permission problem (C1).

**Fix.**

- Report each library's state in `/v1/status`: idle, scanning, syncing, or error, with the last error and the last successful sync per peer.
- Write structured logs with `log/slog`.
- Add a `synk log` command.

### M10. The relay doesn't work, and its security claims are false

**Where.** `relay/worker/wrangler.toml`, `relay/worker/src/index.ts`, `relay/README.md`, and the "Relays" section of `docs/ARCHITECTURE.md`.

**What happens.**

- `wrangler.toml` contains JSON, so `wrangler deploy` fails with "Invalid TOML document: illegal character in key."
- The migration uses `new_classes`. Cloudflare now rejects new key-value-backed Durable Object namespaces on accounts that don't already have one, and the Workers Free plan has only ever supported SQLite-backed ones.
- The daemon has no relay client. `config.Relay` is never read.
- The docs say the relay "splices already-encrypted frames" and "cannot read file bytes." The frames are plaintext (C4), so whoever runs the relay can read everything.
- Rooms are unauthenticated. Anyone who knows or guesses a room name can take one of its two slots, either to block the pair or to sit in the middle.
- Other gaps:
  - No message-size or rate limits.
  - No logs or traces.
  - A hand-written `Env` type instead of `wrangler types`.
  - `webSocketClose` doesn't close the other socket.

**Proof.** A Wrangler 4.148 dry run fails on the committed config. After renaming it to `wrangler.json` and switching to `new_sqlite_classes`, the dry run builds cleanly.

**Fix.**

- Use the corrected config in "Concrete changes."
- Derive the room name and a join credential from the pairing secret, and carry only the encrypted channel from C4.
- Turn on logs and traces, and generate types with `wrangler types`.

Or skip the custom relay entirely. QUIC hole punching with a relay fallback (for example, iroh) or Syncthing's public relays solve NAT traversal without running a service.

### M11. Peers are reachable only at the address seen when pairing

**Where.** Each peer gets one address, recorded at pairing (`internal/engine/session.go:74,239`), and only the first address is ever used (`internal/engine/peers.go:81-88`). There's no discovery.

**What happens.** A DHCP lease change, or a laptop moving between networks, breaks sync permanently.

**Fix.**

- Discover peers on the LAN with Bonjour/mDNS (`_synk._tcp`).
- Update a peer's address after each successful authenticated inbound session.
- Keep several addresses per peer and track which ones work.

### M12. The conflict winner is arbitrary

**Where.** `internal/reconcile/reconcile.go:194-221` picks whichever version has the lexicographically smaller hash.

**What happens.** Both devices pick the same winner, which is the point. But half the time the newer edit moves to the `.synk-conflict-` copy, while the app keeps showing the older content at the original path.

**Fix.** Keep the deterministic tie-break, but prefer the later modification time, then the device ID, as Syncthing does. The time is only a hint for choosing the winner; it isn't used to decide causality.

### M13. CI and the Go toolchain are out of date

**Where.** `.github/workflows/test.yml` and `go.mod`.

**What happens.** CI runs `go test` and `go vet` on Linux with Go 1.22, which has been out of security support since early 2025. That matters more once `crypto/tls` carries the protocol (C4). CI is also missing:

- the race detector;
- a macOS runner, even though macOS is the target platform;
- `gofmt`, `staticcheck`, and `govulncheck`;
- a Swift build;
- any relay check.

**Fix.** See the workflow in "Concrete changes."

## Low findings

- **L1. Repository name.** The README's clone URL and the Go module path are `github.com/Zyniker13/synk`. The repository is `Zyniker13/zSynk`, and the README URL returns 404.
- **L2. Ignore-matcher quirks.** Directory-only patterns such as `bak/` also match files named `bak`, at any depth (`internal/ignore/ignore.go:67-79`). So Logseq's `bak/` pattern hides a user folder like `Projects/bak/`. Also, the watcher skips `.git`, but the scanner syncs it.
- **L3. Leftover folders from interrupted package swaps.** An interrupted swap leaves a `<name>.synk-old` folder behind (`internal/atomicfile/write.go:89`). It isn't ignored, so it would sync as an ordinary folder of loose package contents.
- **L4. Unbounded growth.** `journal.jsonl` is never compacted, and it's re-read in full on every pull (`internal/journal/journal.go:113-132`). Tombstones and version-vector entries never expire.
- **L5. Dead or misleading code.**
  - These are unused: `Node.Busy`, `idOr`, `ExpectPair`, `scan.ManifestHash`, the `_ = mh` line in `scanPackage`, and the `PackageExts` field on presets.
  - The identity comments say keys are base64; they're base32.
  - `.photoslibrary` is listed as a syncable package, which invites syncing a Photos library. That should be refused outright.
- **L6. Test gaps.** There are 55 unit tests, with statement coverage between 28% and 82% per package. `cmd/synk` and `internal/config` have no tests. Most engine tests use the in-memory `SyncOnce` shortcut, so the real network session has thin coverage. That session is where H5, C4, and the races live.
- **L7. Renames and folders.** A rename is a delete plus an add, so moving a 2 GB package costs a 2 GB vault copy plus a 2 GB rewrite on every peer. Empty directories never sync, and deleting a folder leaves empty directories behind on peers.
- **L8. `doctor` reports less than documented.** It reports missing folders, file counts, and whether any device is paired. The threat model also promises "placeholder hits," and it reports neither those nor scan errors.

## Docs vs. reality

| Claim (where) | Today | Finding |
| --- | --- | --- |
| "Never propagate a delete from an incomplete scan." (README rule 1) | Unreadable folders, symlinked roots, and newly ignored files all propagate deletes | C1 |
| "iCloud placeholders are never treated as deletes." (README, ARCHITECTURE, THREAT_MODEL) | `.note.md.icloud` is mapped to `.note.md`, so `note.md` is deleted on peers | C2 |
| "Partial scan / permission error → path marked incomplete; no tombstone" (THREAT_MODEL) | Only unreadable files are marked incomplete; unreadable folders tombstone their contents | C1 |
| "Snapshot before overwrite." / "Every overwrite is snapshotted." (README) | Snapshot failures are ignored | H1 |
| "Packages are copied as one unit so they never tear." (Pages preset hint) | Apply is atomic, but a package can be captured mid-save | H4 |
| "Synk waits until the package is idle." (Keynote preset) | Not implemented (`Node.Busy` is unused) | H4 |
| "Pairing + signed LAN transport: Tested" (README status) | Only a header tuple is signed; traffic is plaintext and replayable | C4 |
| "Loopback API abuse → rejects non-loopback remotes" (THREAT_MODEL) | Browsers and other local users are loopback clients | C5 |
| "Scan a QR on the other device." (ARCHITECTURE) | No QR rendering or scanning; the QR's fingerprint is never checked | H8 |
| "The relay cannot read file bytes." / "splices already-encrypted frames" (ARCHITECTURE, relay README) | Frames are plaintext; no relay client exists; the config doesn't deploy | M10 |
| "The companion app syncs while it is open." (README); "Runs the same engine…" (ARCHITECTURE) | The iOS app has no engine or networking; its Pair button is cosmetic | H12 |
| "If the daemon is not running, the app starts it." (XCODE.md) | Not implemented | H12 |
| "the iOS app uses the JSON protocol … against a paired Mac on the LAN" (XCODE.md) | The iOS app doesn't use `SynkClient`, and the API only answers loopback | H12 |
| "Refuse iCloud Drive vaults" / "the UI warns and offers to move" (APPS.md) | No check; the CLI defaults into iCloud | M1 |
| "Large media is content-addressed and only sent once." (Keynote preset hint) | Any file over 32 MiB never syncs | H5 |
| "`synk doctor` reports … placeholder hits" (THREAT_MODEL) | It doesn't | L8 |
| `git clone https://github.com/Zyniker13/synk` (README) | 404; the repository is `zSynk` | L1 |

## What's working well

These are worth keeping as the code changes around them:

- **Causality model.** Version vectors decide ordering, never wall clocks. A delete only wins when its vector dominates the edit's, and concurrent delete-versus-edit keeps the edit.
- **Deterministic conflicts.** Both peers compute the same conflict file name from the losing hash, so conflicts don't ping-pong. Package conflict copies stay packages, because the extension is preserved.
- **Tombstone discipline in `Reconcile`.** A path that's missing from the remote index is never treated as deleted; deletes must be explicit tombstones. The bugs in C1 are all on the local scan side.
- **Integrity checks.** Blobs are verified on every read, peer bytes that don't match their hash go to quarantine instead of the library, and a materialized package is re-verified against its manifest before the swap.
- **Write path.** Every write goes through temp file, fsync, rename, and directory fsync, and there's a free-space check before apply.
- **Pairing secrets.** The 128-bit pairing token is compared in constant time, the pairing offer expires and is single-use, and the identity file is self-checking (the device ID must match the key).
- **Code quality.** Small, single-purpose packages with no third-party dependencies make the code easy to audit. The unit tests cover the reconcile rules well (`internal/reconcile` has 14 tests).

## Strategic recommendations

### 1. Make "never lose a file" an engineering property, not a promise

Each README safety rule should become a named invariant, enforced by tests:

- **Unit tests.** The `make assess` cases are a start.
- **Property-based tests.** For example: "`Advance` never tombstones a path that exists on disk."
- **Fault injection:**
  - kill the daemon at each journal step;
  - fill the disk using a size-limited tmpfs;
  - deny access to subtrees;
  - run slow and dropped peers;
  - jump the clock.

Add a mass-delete brake (C1). Make the first sync of a library merge rather than mirror: it should never delete anything.

### 2. Keep the engine, but stop hand-rolling the transport

| Option | Keeps package and placeholder safety? | Notes |
| --- | --- | --- |
| Embed Syncthing as the replicator | No | Syncthing applies interior files itself. Synk can't make package swaps atomic, or veto placeholder deletes, from outside it. |
| Keep Synk's engine; carry it over TLS 1.3 with pinned Ed25519 keys | Yes | Standard library only, auditable, smallest change. **Recommended now.** |
| Keep Synk's engine; carry it over a P2P stack (iroh, libp2p) | Yes | Brings NAT traversal and relays, at the cost of a large dependency. Revisit for remote sync (Phase 6). |

Separately, borrow from Syncthing's published designs instead of reinventing them:

- block sizes and block lists (the Block Exchange Protocol);
- index sequence numbers;
- "ignored is not deleted" semantics;
- case-collision handling;
- folder markers (`.stfolder`);
- staggered versioning.

### 3. One writer per data directory

The daemon should be the only process that touches `SYNK_DIR`, and it should hold a file lock on it. The CLI and the apps become clients over an authenticated local channel: a Unix socket with a check of the caller's user ID, or a bearer token over loopback HTTP. Add an event stream so the UI updates without polling. This fixes H9, H10, and most of C5 by construction.

### 4. A storage model that scales

- Content addressing at the block level, with reference-counted garbage collection.
- Index entries that carry the block list, the metadata (mode, mtime, allowlisted extended attributes), and, locally only, the stat-cache fields.
- A vault made of retained references rather than copies; restoring means materializing from blocks.
- Staging on the library's own volume.

Together these fix H5, H6, H7, M2, M3, and M4.

### 5. Make the Apple platform decisions early

- **macOS distribution.** A sandboxed app can only open the folders the user grants it, through security-scoped bookmarks, and the Go daemon can't use those. `XCODE.md` currently suggests turning on App Sandbox, which would leave the daemon unable to read library folders unless the app hands access to it. The simplest path is a Developer ID build with notarization and an unsandboxed login item registered through `SMAppService`. Expect macOS privacy (TCC) prompts for Documents, Desktop, and Downloads.
- **File coordination.** Read and write packages through `NSFileCoordinator` where possible, so a document isn't swapped out from under the app that has it open (H4).
- **Change detection.** Use FSEvents (H6).
- **iOS feasibility for each target app.** A File Provider only helps apps that open documents from other providers. Pages, Keynote, Numbers, and iA Writer (through Locations) do, and Ulysses supports external folders. As far as I know, Obsidian on iOS opens vaults only from its own folder ("On My iPhone › Obsidian") or from iCloud Drive. For apps like that, the workable approach is the one existing iOS Syncthing clients (such as Möbius Sync) use: the user picks a folder inside the app's own Files location, and Synk syncs into it through a security-scoped bookmark. Check this per app before building the File Provider.

### 6. Correct the docs now

Until the fixes land, update the README status table and the architecture doc to match the "Docs vs. reality" table above. In particular, remove "the relay cannot read file bytes," "signed LAN transport: Tested," and the iOS sync claims. Users and contributors will make decisions based on those lines.

## Roadmap

Each phase has an exit criterion that can be checked. Phases 0 and 1 should be done before anyone syncs real documents with Synk.

### Phase 0: Stop data loss and close the open doors

- **Scope:**
  - C1, C2, C3, C5, H1, H2, H3, H9.
  - The parts of H11 and H12 that block the Mac app from working at all: preset IDs, `[]` instead of `null`, and HTTP status checks.
  - M1 and M2.
  - The doc corrections in strategic recommendation 6.
  - CI with `-race` and a macOS runner.
- **Exit criterion:** the matching `make assess` tests pass and have moved into `go test ./...`, the mass-delete brake exists, and the README makes no claim the code doesn't meet.

Several Phase 0 items are small, contained changes:

- `Recover` skips records from other libraries.
- `Status` encodes `[]`.
- Placeholder names map correctly.
- The library root is resolved through symlinks.
- A failed snapshot aborts the write.
- The API checks Host and Origin.
- Library IDs equal the preset ID.
- The Obsidian iCloud default is removed.
- The relay config is renamed.

### Phase 1: Secure transport and pairing

- **Scope:**
  - C4, H8, M6, M8, and the address-update part of M11.
  - A versioned protocol, documented in `docs/PROTOCOL.md`.
  - An updated threat model that covers the browser, other local users, an attacker on the network path, and a compromised paired device.
- **Exit criterion:** tests with an attacker relaying traffic between two devices show that replay, tampering, and man-in-the-middle can neither read nor change anything. Pairing pins the QR fingerprint, and library sharing is explicit.

### Phase 2: Scale and efficiency

- **Scope:**
  - H4, H5, H6, and H7.
  - M3: retention and restore.
  - M4: metadata.
  - L4, and the rename detection part of L7 (reusing blocks so a rename doesn't recopy data).
- **Exit criterion:**
  - A 4 GB file syncs, and resumes after an interruption.
  - An idle daemon with a 20 GB library reads close to zero bytes per minute.
  - Memory use doesn't depend on file size.
  - `SYNK_DIR` overhead is bounded by the retained versions plus a small fixed share of library size.

### Phase 3: The daemon as single writer, and observable

- **Scope:**
  - H10, M7, and M9.
  - Local socket authentication and an event stream.
  - New CLI commands: `versions`, `restore`, `pause`, `resume`, `unpair`, `remove`, and `log`.
  - Journal compaction.
- **Exit criterion:** the CLI and the apps never write `SYNK_DIR` directly, and every failure shows up in status with a reason a person can act on.

### Phase 4: Mac app MVP

- **Scope:**
  - The daemon bundled in the app and registered with `SMAppService`.
  - Onboarding: name the device, pick apps, pair by code or QR, with Bonjour discovery.
  - A conflict center, a restore UI, and the iCloud "move" flow.
  - Developer ID signing, notarization, and an update channel.
  - The Xcode project in the repository and built in CI.
  - M5, tested on APFS.
- **Exit criterion:** a non-technical user can pair two Macs and sync Obsidian and Keynote without opening a terminal.

### Phase 5: iOS

- **Scope:**
  - First, check per app which iOS integration is possible (strategic recommendation 5).
  - A public `mobile` facade package for gomobile. `gomobile bind` can't bind `internal/engine` from outside the module, which is the command `XCODE.md` suggests.
  - Syncing into user-picked folders through security-scoped bookmarks, and/or a replicated File Provider extension.
  - Background scheduling with `BGTaskScheduler`.
  - QR pairing with the camera.
- **Exit criterion:** iPhone-to-Mac sync works for iWork and, if feasible, Obsidian, with the docs honest about background behavior.

### Phase 6: Beyond the LAN and beyond two devices

- **Scope:**
  - NAT traversal: QUIC hole punching plus an authenticated relay, or iroh.
  - Tested topologies with three or more devices.
  - Selective sync.
  - Optional encryption of the vault at rest.

## Concrete changes

These sketches show the shape of the main fixes. They aren't drop-in patches.

**Path containment (C3).** Validate where data enters from the network, then do the file operations through `os.Root`:

```go
// internal/safepath
var ErrUnsafe = errors.New("safepath: path is not inside the library")

func Check(rel string) error {
	if rel == "." || !filepath.IsLocal(filepath.FromSlash(rel)) || path.Clean(rel) != rel ||
		strings.ContainsAny(rel, "\\\x00") {
		return ErrUnsafe
	}
	// APFS is case-insensitive by default, so ".SYNK" names the same directory.
	if first, _, _ := strings.Cut(rel, "/"); strings.EqualFold(first, ".synk") {
		return ErrUnsafe
	}
	return nil
}
```

```go
// apply: every create, rename, and remove goes through the library root.
root, err := os.OpenRoot(lib.Path) // Go 1.24+; Rename/MkdirAll/RemoveAll need 1.25+
if err != nil {
	return err
}
defer root.Close()
if err := root.MkdirAll(path.Dir(op.Path), 0o700); err != nil {
	return err
}
return root.Rename(path.Join(".synk/staging", id, "file"), op.Path)
```

**Scan coverage (C1).** Resolve the root, record unreadable folders, and don't tombstone anything under them:

```go
// scan.Library
root, err := filepath.EvalSymlinks(root)
if err != nil {
	return nil, err
}
err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
	if err != nil {
		rel, _ := filepath.Rel(root, p)
		res.Unreadable = append(res.Unreadable, filepath.ToSlash(rel))
		res.Errors = append(res.Errors, err.Error())
		return nil
	}
	// ...
})
```

```go
// reconcile.Advance, for a previous entry missing from the scan
if !covered(path, unreadable) {
	old.Incomplete = true
	next.Entries[path] = old
	continue
}

func covered(p string, unreadable []string) bool {
	for _, dir := range unreadable {
		if dir == "." || p == dir || strings.HasPrefix(p, dir+"/") {
			return false
		}
	}
	return true
}
```

**iCloud stub names (C2):**

```go
// placeholderTarget maps iCloud's "dir/.name.icloud" stub to "dir/name".
func placeholderTarget(rel string) (string, bool) {
	dir, base := path.Split(rel)
	name, ok := strings.CutSuffix(base, ".icloud")
	if !ok || len(name) < 2 || name[0] != '.' {
		return "", false
	}
	return dir + name[1:], true
}
```

**Local API guard (C5).** This goes inside the existing loopback check. The Swift client must then send the token, and must set `Content-Type` on every POST, including `pair/start`.

```go
func guard(token, port string, next http.Handler) http.Handler {
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[r.Host] || r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
```

**Snapshot or abort (H1), recovery scope (H2), and re-check before replace (H3):**

```go
var ErrChangedSinceScan = errors.New("apply: destination changed since the last scan")

// applySet, after staging. applyDelete does the same with "pre-delete".
backup := ""
if fileExists(dest) {
	want := currentHash(idx, op.Path)
	if !unchangedSinceScan(dest, want) {
		return e.abort(rec.ID, "set", op.Path, ErrChangedSinceScan)
	}
	snapshot := e.Vault.SnapshotFile
	if isDir(dest) {
		snapshot = e.Vault.SnapshotDir
	}
	snap, err := snapshot(e.FolderID, op.Path, dest, want, "pre-overwrite")
	if err != nil {
		return e.abort(rec.ID, "set", op.Path, fmt.Errorf("apply: not replacing %s without a snapshot: %w", op.Path, err))
	}
	if isDir(dest) {
		backup = filepath.Join(snap.Dir, "content")
	}
}

// A size, mtime, and inode comparison should come first; hash only when it's inconclusive.
func unchangedSinceScan(dest, want string) bool {
	if isDir(dest) {
		_, raw, err := packages.BuildManifest(dest, ignore.Compile(ignore.GlobalPatterns()))
		return err == nil && packages.HashManifest(raw) == want
	}
	got, _, err := packages.HashFile(dest)
	return err == nil && got == want
}

func (e *Env) abort(id, op, path string, err error) error {
	_, _ = e.Journal.Append(journal.Record{ID: id, Status: journal.StatusAborted, FolderID: e.FolderID, Op: op, Path: path, Note: err.Error()})
	return err
}
```

```go
// Env.Apply: today the first error stops the batch. Skip a changed file instead;
// the next round sees the local edit and produces a conflict.
for i, op := range ops {
	err := e.applyOne(op, idx, fmt.Sprintf("%s-%d", e.FolderID, i))
	if errors.Is(err, ErrChangedSinceScan) {
		continue
	}
	if err != nil {
		return err
	}
}
```

```go
// Env.Recover
for _, r := range inc {
	if r.FolderID != e.FolderID {
		continue
	}
	// ...
}
```

**Status JSON (H12, H9).** Copying the slice also removes the aliasing race:

```go
st := Status{
	DeviceID:  n.Device.DeviceID,
	Name:      n.Device.Name,
	Listen:    listen,
	API:       n.Config.API,
	Libraries: []LibraryState{},
	Peers:     append([]config.Peer{}, n.Config.Peers...),
}
```

```swift
// An extension keeps SynkStatus's memberwise initializer. It must live in
// SynkClient.swift, because the synthesized CodingKeys is private.
extension SynkStatus {
    // Older daemons send null for empty lists.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        deviceId = try c.decode(String.self, forKey: .deviceId)
        name = try c.decode(String.self, forKey: .name)
        libraries = try c.decodeIfPresent([LibraryState].self, forKey: .libraries) ?? []
        peers = try c.decodeIfPresent([Peer].self, forKey: .peers) ?? []
    }
}

enum SynkError: Error {
    case http(status: Int, body: String)
}

extension SynkClient {
    // Every call goes through here, so it always carries the API token (C5) and a
    // 4xx or 5xx reply surfaces as an error instead of a silent success.
    func send(_ request: URLRequest, token: String) async throws -> Data {
        var req = request
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            throw SynkError.http(status: http.statusCode, body: String(decoding: data, as: UTF8.self))
        }
        return data
    }
}
```

**Transport (C4, H8).** Mutual TLS 1.3 using each device's Ed25519 key, pinned from the pairing record. During pairing, the joiner pins the fingerprint from the QR code. The offering device requires the token inside the encrypted channel, bound to the session with `ConnectionState().ExportKeyingMaterial`.

```go
func peerTLS(dev *identity.Device, pinned func(ed25519.PublicKey) error) (*tls.Config, error) {
	priv, err := dev.Private()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dev.DeviceID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}},
		ClientAuth:   tls.RequireAnyClientCert,
		// Peers are authenticated by pinned key below, not by a CA chain.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no peer certificate")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			pub, ok := cert.PublicKey.(ed25519.PublicKey)
			if !ok {
				return errors.New("peer key is not Ed25519")
			}
			return pinned(pub)
		},
	}, nil
}
```

**Relay config (M10).** Rename `relay/worker/wrangler.toml` to `wrangler.json`. This version passes `wrangler deploy --dry-run` with Wrangler 4.148:

```json
{
  "name": "synk-relay",
  "main": "src/index.ts",
  "compatibility_date": "2026-10-07",
  "observability": { "enabled": true, "traces": { "enabled": true } },
  "durable_objects": {
    "bindings": [{ "name": "ROOM", "class_name": "Room" }]
  },
  "migrations": [{ "tag": "v1", "new_sqlite_classes": ["Room"] }]
}
```

**CI (M13).** Also raise `go.mod` to a supported Go release; 1.25 or newer gives the `os.Root` methods used above.

```yaml
name: test
on: [push, pull_request]
jobs:
  go:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: test -z "$(gofmt -l .)"
      - run: go vet ./...
      - run: go test -race ./...
      - run: go run honnef.co/go/tools/cmd/staticcheck@latest ./...
      - run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...
  assessment:
    runs-on: ubuntu-latest
    continue-on-error: true # burn-down list; fails until Phase 0 lands
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make assess
  relay:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: relay/worker
    steps:
      - uses: actions/checkout@v4
      - run: npx --yes wrangler@4 deploy --dry-run
```

## Engineering process

- **Use the reproduction tests as the burn-down list.** When a fix makes an `assessment` test pass, remove the build tag in the same PR so the test guards against regressions.
- **Fuzz the parsers and the rules.** `go test -fuzz` targets for:
  - `safepath.Check`;
  - frame and message decoding;
  - `Reconcile`/`Advance`, against property checks: no delete without a dominating tombstone, convergence after N rounds, and no tombstone for a path that exists on disk.
- **Test the real network path.** Run multi-node tests over real TCP, then TLS, instead of `SyncOnce`. Include a test harness that relays traffic between two devices so tampering and replay can be exercised. Run on a case-insensitive APFS volume in macOS CI.
- **Kill the daemon on purpose.** A crash-point harness that runs `kill -9` at every journal state, then asserts recovery is correct and confined to its library.
- **Give delete paths extra review.** Any change that can create a tombstone or call `RemoveAll` should name the invariant test that covers it.
- **Publish security basics before the first external build:** a `SECURITY.md` with a private disclosure address, a refreshed `THREAT_MODEL.md`, and `docs/PROTOCOL.md`.

## Reproduction index

Run all of these with `make assess`, as a non-root user (root ignores the `chmod` restrictions two of the tests rely on).

- `TestAssessPackageAppliesAcrossVolumes` needs a second filesystem. It uses `/dev/shm` by default; on a Mac, point `SYNK_ASSESS_OTHER_VOLUME` at a mounted disk image.
- `TestAssessIdleDaemonDoesNotRehashUnchangedFiles` reads `/proc/self/io`, and skips on systems that don't have it.

| Test | Package | Finding |
| --- | --- | --- |
| `TestAssessUnreadableDirDoesNotPropagateDeletes` | engine | C1 |
| `TestAssessSymlinkedLibraryRootDoesNotPropagateDeletes` | engine | C1 |
| `TestAssessNewlyIgnoredFileIsNotDeletedOnPeers` | engine | C1 |
| `TestAssessICloudEvictionIsNotADelete` | engine | C2 |
| `TestAssessRemoteIndexCannotEscapeLibraryRoot` | engine | C3 |
| `TestAssessPeerBlobHashCannotEscapeQuarantine` | engine | C3 |
| `TestAssessIndexSignatureCoversPayload` | engine | C4 |
| `TestAssessReplayedHelloCannotReadFiles` | engine | C4 |
| `TestAssessCrossOriginPostRejected` | api | C5 |
| `TestAssessForeignHostRejected` | api | C5 |
| `TestAssessOverwriteAbortsWhenSnapshotFails` | apply | H1 |
| `TestAssessRecoverOnlyTouchesItsOwnLibrary` | apply | H2 |
| `TestAssessApplyRefusesToOverwriteUnscannedEdit` | apply | H3 |
| `TestAssessLargeFileSyncsOverNetwork` | engine | H5 |
| `TestAssessIdleDaemonDoesNotRehashUnchangedFiles` | engine | H6 |
| `TestAssessDaemonLoopRaceFree` | engine | H9 |
| `TestAssessCLIChangesSurviveRunningDaemon` | engine | H10 |
| `TestAssessAppEnabledLibrariesShareAnID` | api | H11 |
| `TestAssessStatusEncodesEmptyArrays` | api | H12 |
| `TestAssessObsidianDefaultIsNotICloud` | apps | M1 |
| `TestAssessPackageAppliesAcrossVolumes` | engine | M2 |
