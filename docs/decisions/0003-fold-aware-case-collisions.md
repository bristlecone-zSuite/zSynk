# 0003: Case-colliding paths on volumes that fold case

Date: 2026-11-16

## Context

A real-device probe (sender on a case-sensitive APFS volume, receiver on a
case-insensitive APFS volume) found a silent data loss. When a peer's index
held two spellings of one name (`Note.md` and `note.md`) with different
content, the receiving library — whose volume folds case — could keep only
one file. The losing spelling was deferred every round: its content never
landed anywhere, and sync reported success. README rule 5 says no version
may be dropped without a conflict copy, so this was a safety bug, not a
cosmetic one.

Two smaller defects surfaced while fixing it:

- The volume probe (`fsmeta.FoldsCase`) removed its probe file before
  checking for it, so every volume looked case-sensitive and the engine
  never took the folding path.
- The apply-side backstop (a set whose target path is taken on disk by
  another entry's file) existed for the general class but could not fire
  correctly without fold awareness at reconcile time.

## Decision

1. Probe the volume, don't assume it. `fsmeta.FoldsCase` creates a probe
   file, checks it under a case-swapped name, and cleans up. A probe error
   counts as case-sensitive — the answer that never moves a path.
2. When a library's volume folds case, the engine rewrites a peer's index
   before reconcile (`Node.remoteFor` → `foldAdjust`): a remote path that
   folds onto exactly one live local path is compared as that path, so
   reconcile resolves the pair by vector like any concurrent edit — winner
   at the path, loser as a conflict copy.
3. Distinct live paths in one fold group: the keeper is the path a live
   local entry holds, else the first in sort order. Other entries are
   dropped when the keeper already holds their content, and otherwise
   become sets at conflict names that borrow the keeper's spelling, with
   the entry's own content hash in the name. Conflict names for one fold
   group therefore can't collide with each other, and two devices that
   spell the local file differently still converge, because the alias step
   matches conflict names by fold key, not by exact spelling.
4. The apply backstop stays as the last line for classes this misses
   (normalization, hard links): a set that would silently replace a file
   claimed by a live entry becomes a conflict copy beside it.

Case-sensitivity is per volume and probed at runtime, so no configuration
and no platform #ifdef is involved. Library paths are probed lazily and
cached per process; the test hook `FoldProbe` overrides the probe.

## Consequences

- Rule 5 holds on folding volumes: every version of a fold-colliding name
  is either at a path or at a conflict copy; sync reports success only
  when nothing is left deferred.
- A non-folding peer receives the keeper-spelled conflict name as an
  ordinary file and keeps it beside its originals — a redundant but safe
  copy, and the state is quiet afterwards.
- `foldsCase` results are cached per process; if a library is moved to a
  volume with different folding between restarts, the next process picks
  up the change. Within a process the cache is authoritative, which keeps
  reconcile deterministic.
- Tests: `internal/engine/fold_test.go` (unit), `internal/engine/casecollision_test.go`
  (end-to-end, with an optional case-sensitive volume via
  `SYNK_CASE_SENSITIVE_DIR`), `internal/fsmeta/fold_test.go` (probe
  against ground truth it establishes independently).
