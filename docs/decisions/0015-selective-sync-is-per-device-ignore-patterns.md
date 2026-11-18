# 0015 — Selective sync is per-device library ignore patterns

## Context

Phase 6 calls for selective sync. A person may want one device to leave a
subtree alone — an archive, a scratch folder — while the others sync it in
full. The engine already had every piece except a way to set it: the
scanner and the sync ops path both consult an ignore matcher built from the
global patterns plus the preset's, and `reconcile.Advance` keeps a
previously tracked path that the matcher now ignores as `Incomplete`
instead of recording a deletion. Patterns that live in the library's own
config on this device only reuse all of that safely.

## Decision

A library carries `Ignore []string` in its config on each device, applied
after the global and the preset's patterns, last match wins. They are set
per device — engine method `SetLibraryIgnore`, API
`POST /v1/libraries/{id}/ignore`, CLI `synk ignore --library ID [--add
PAT]... [--remove PAT]... [--clear]` — and never travel the wire: sync
exchanges indexes, not library configs, so one device's patterns cannot
silence another's.

Semantics, stated honestly rather than cleverly:

- An ignored directory is not scanned into, so a re-include (`!pattern`)
  cannot rescue paths under an excluded directory. Re-includes work for
  paths outside excluded directories.
- Ignoring a path that already synced keeps the local copy and its index
  entry. A pattern must never turn into a deletion for anyone: the peer's
  copy stays, and this device's own delete of an ignored path propagates
  neither.
- `EnableLibrary` keeps the patterns an update carries none of, so callers
  that don't know the field cannot un-ignore files.

## Consequences

- Selective sync works today on every platform the CLI or the API reaches,
  including through the daemon; the iOS and Mac apps read the patterns
  back in status and can set them through the API.
- Because ignored directories aren't scanned, a device that ignores a
  subtree still sends and accepts ops for its own pre-pattern history of
  that subtree — frozen as `Incomplete` — which is exactly what keeps
  deletions from propagating.
- If someone later wants git's "parent dirs must be un-excluded to
  re-include" subtleties, that is a change to `internal/ignore`'s matcher
  and its own decision; the scanner's directory pruning would need the
  same change to stay consistent.
