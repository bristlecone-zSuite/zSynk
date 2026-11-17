# 0010: iOS libraries live in the app sandbox

Date: 2026-11-17

## Context

The roadmap's Phase 5 says "sync into user-picked folders and/or a File
Provider extension". On a Mac, a person picks any folder. On iOS an app
can write only inside its own sandbox. Two mechanisms exist to reach
further out:

- a security-scoped bookmark to a folder the user picked once, which the
  app must re-resolve every time it runs — and iOS may suspend or kill
  the process between syncs, so every background wake risks losing scope
  for exactly the folder that holds the person's only copy;
- a File Provider extension, which exposes the synced tree natively in
  Files and to every app that reads files, but is a separate target with
  its own lifecycle (planned next in Phase 5).

The apps that matter read their libraries through Files: Obsidian on
iOS opens a vault stored in another app's container ("Open from
Files"), and iWork documents open from Files directly. So the files
must be reachable in Files, but they do not need to be anywhere exotic.

## Decision

On iOS, each library lives in the app's own Documents folder, one
folder per app preset: `Documents/Obsidian`, `Documents/Keynote`, and
so on. Adding a library in the app means picking the app preset, not a
path; the model creates the folder and calls the engine's add-library
bridge with that path. The Info.plist keys `UIFileSharingEnabled` and
`LSSupportsOpeningDocumentsInPlace` make Documents visible and openable
as Files → On My iPhone → Synk.

The app does not offer picking folders outside its sandbox, and does
not use security-scoped bookmarks.

## Consequences

- Sync works with the engine's own safety rules untouched: the
  libraries are ordinary local folders the app process can always read,
  wake after wake.
- Obsidian and iWork reach their files through Files, which is the
  integration those apps support on iOS anyway.
- What the user sees is a single root per device, not the Mac's
  free-form folders; per-preset folders inside Documents keep the two
  sides' library IDs aligned (the preset's ID is the library ID).
- Background limits are real: the embedded engine's watch loop runs
  while the app process runs, so the docs must say iOS syncs when the
  app is in use (and later, with the scheduled-refresh work), not
  continuously.
- The File Provider extension remains the next step for deeper Files
  integration; nothing here blocks it, because the engine already
  treats the sandbox folder as an ordinary library.
