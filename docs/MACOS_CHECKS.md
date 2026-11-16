# macOS Checks — Phase 0

This document records what was checked on a real Mac (macOS 27.2.1) for
the macOS-specific items the Phase 0 assessment flagged.

## iCloud dataless files

**Finding C2.** iCloud replaces an evicted file with a stub; on macOS 14+
it may use dataless files (SF\_DATALESS flag) instead.

**What the code does.**

- `internal/scan/dataless_darwin.go` checks `st_flags & 0x40000000`
  (SF\_DATALESS) and marks the file `Incomplete` without reading it,
  so it never triggers a cloud download during a scan.
- `internal/scan/scan.go:451-461` maps `dir/.name.icloud` stubs to
  `dir/name` and marks the target `Incomplete` if no real file exists.
- `internal/apps/catalog.go:295-315` refuses any library path under
  `~/Library/Mobile Documents` or `~/Library/CloudStorage` with a
  clear error; `DefaultPath` returns `ErrNoFolder` for cloud paths.

**Checked on this Mac.** No iCloud Drive is signed in (`Library/Mobile
Documents` and `Library/CloudStorage` do not exist), so a live dataless
file cannot be created. The unit test `TestDatalessFilesAreNotRead`
replaces the probe with a deterministic predicate and confirms that
dataless files are not read and dataless folders stop the walk without
removing their children. `TestPlaceholders` confirms the `.icloud` stub
mapping.

**Verdict.** The code is correct for both stub and dataless forms.
Without live iCloud on this machine the dataless path was not exercised
end-to-end; a device with iCloud Drive should be tested for the live
download-trigger path.

## APFS case handling

**Finding M12 / case-collision.** A peer's index holding two spellings
of one name (`Note.md` and `note.md`) with different content could cause
the receiving library — whose volume folds case — to keep only one file
and silently drop the other.

**What the code does.**

- `internal/fsmeta/fold.go` probes the volume by creating a file,
  checking it under a case-swapped name, and cleaning up. A probe
  error counts as case-sensitive.
- `internal/engine/fold.go` rewrites a peer's index before reconcile:
  a remote path that folds onto exactly one live local path is compared
  as that path, so the pair resolves by vector. The loser becomes a set
  at a conflict name borrowed from the keeper's spelling.
- `internal/apply/apply.go` keeps the backstop: a set that would
  silently replace another entry's file becomes a conflict copy.
- `docs/decisions/0003-fold-aware-case-collisions.md` records the
  design and the convergence proof.

**Checked on this Mac.** This Mac's APFS volume folds case. A probe
created a temp file, verified the case-swapped name was the same file,
and confirmed cleanup worked. The end-to-end test `TestCaseCollisionKeepsBothVersions`
runs with the sender on a case-sensitive APFS volume (a separate
case-sensitive directory on this Mac) and the receiver on the folding
volume; it confirms both spellings land and sync reports success.
`TestCaseDrift` confirms the mixed case converges without spinning
(3 sync rounds, no new ops).

**Verdict.** Confirmed on this Mac. Both the probe and the fold-aware
reconcile work as designed.

## Privacy denials (TCC)

**Finding C1.** `filepath.WalkDir` reports an error when it cannot read
a directory (permission denied, TCC denial for Documents or Desktop,
I/O error). The scan must not tombstone the directory's contents.

**What the code does.**

- `internal/scan/scan.go:162-170`: when WalkDir returns an error for a
  non-root path, the path is recorded in `Result.Unconfirmed` and
  `Result.Errors`, and the walk skips the directory.
- `internal/scan/scan.go:196-198`: when `d.Info()` fails (e.g. the
  file itself is permission-denied), the path is unconfirmed and the
  walk skips the directory.
- `internal/reconcile/advance.go`: entries under unconfirmed paths are
  kept as `Incomplete` rather than tombstoned.

**Checked on this Mac.** The test `TestScanReportsWhatItCannotRead`
creates a library with a subdirectory whose permissions are set to
`0o000`. The scan reports the directory in `Unconfirmed`, records an
error, and continues scanning the rest of the library. Other files are
reported normally.

**Verdict.** Confirmed. TCC denials behave the same as permission
denials in `filepath.WalkDir`, so the same code path covers them.
