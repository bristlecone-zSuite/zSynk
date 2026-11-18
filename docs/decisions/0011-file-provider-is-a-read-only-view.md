# 0011: The File Provider extension is a read-only view of the app group container

Date: 2026-11-17

## Context

Phase 5 asks for "the File Provider extension so that Files, On My
iPhone, Synk works". Decision 0010 put each library in the app's own
Documents folder, which Files already shows. A File Provider extension
gives a deeper integration: the synced tree appears as a native
location (On My iPhone → Synk) that stays current as the engine syncs,
and that the system can hand to other apps.

The design question is who owns the files. The two options:

- the extension owns the tree and the engine writes into the extension's
  domain. This is the pattern Apple's own apps use, but it means the
  engine — which runs in the app process, not the extension — must
  write through the File Provider API, and every write becomes a
  provider change the system has to apply. The engine's safety rules
  (atomic writes, versions kept on overwrite) would have to be
  re-expressed through the provider, and a bug in that path could
  corrupt the only copy.
- the engine keeps writing real files exactly as it does today, into a
  folder the app process owns, and the extension is a *view* over those
  files: it enumerates them and publishes change notifications, and
  when the system asks for a file's content it copies the real file.

The second option keeps the engine's write path — the one the tests
prove safe — completely untouched. The extension never writes, never
runs the engine, and can be deleted without touching a single byte of
the synced data.

The files must be reachable from both the app process (which runs the
engine) and the extension process (which serves the view). An app group
container is the standard mechanism: both targets are entitled to
`group.com.zyniker.synk` and see the same `Documents` folder. When the
entitlement is unavailable (an unsigned or ad-hoc build), the model
falls back to the app's own Documents folder, so the decision 0010
behavior keeps working.

## Decision

- The engine syncs each library into the app group container's
  `Documents` folder, one folder per app preset, exactly as before
  (decision 0010, new location). The app group is
  `group.com.zyniker.synk`.
- The File Provider extension (`SynkFileProvider`, bundle
  `com.zyniker.synk.fileprovider`, point
  `com.apple.fileprovider-nonui`) is a read-only view of that folder.
  It enumerates the tree with the shared model and never runs the
  engine.
- The shared model (`apps/shared/FileProviderModel.swift`) compiles
  into both the app and the extension: it walks the libraries folder
  into a snapshot of items (id, parent, name, size, mtime), diffs two
  snapshots into inserts/updates/removals, and builds the
  `NSFileProviderChange` objects. Item ids are
  `synk/<library>/<path>` with every path component percent-encoded,
  so no filename can collide with a separator.
- The app publishes changes on every refresh and, while it runs,
  through a file watcher on the libraries folder (debounced one
  second). iOS does not guarantee background execution, so the view is
  freshest while the app is open or has just been; the docs say so.
- Materialization is a copy from the shared container: when the system
  asks for a file, the extension hands it the real file's URL (the
  container is readable by both processes), so no data is duplicated
  at rest.
- The extension's sync anchor is a fixed token (`synk-v1`): the app
  publishes the full change set each time, so the anchor only needs to
  say "the view is consistent as of now".

## Consequences

- The engine's write path and its safety rules are untouched; the
  File Provider is a layer on top, and deleting the extension deletes
  no data.
- Files shows On My iPhone → Synk with the synced libraries, current
  while the app runs.
- The app group entitlement is required for the new location; builds
  without it fall back to the app's own Documents and the old Files
  exposure keeps working.
- The extension is read-only by design: edits made in another app
  through the view are not supported in 0.1.0. The Mac remains the
  place documents are edited; the phone is the place they are read and
  carried.
- The shared model is unit-tested on macOS (snapshot, diff, id
  encoding, change kinds) and its plumbing is tested on the iOS
  simulator (the system accepts the published changes).
