# 0004: Resumable blob transfers by offset, not block splitting

Date: 2026-11-16

## Context

Phase 2's exit criterion (from the assessment's H5) says a 4 GB file syncs
and resumes after an interruption. The assessment's suggested fix was to
split files into blocks, "which also gives resumable transfers". The
implemented H5 fix took a different path: a file is still one blob, but
the transfer streams it in 1 MiB frames into a temporary file that is
renamed into the store only when its whole hash verifies. That solved the
32 MiB frame limit and the whole-file-in-RAM problem, but an interrupted
transfer throws its partial work away and the next session starts over
from byte 0. For a 4 GB file on a network that drops every few minutes,
starting over can mean it never arrives.

## Decision

Resume at the blob level, by offset, without changing how files are
indexed:

- The receiver stages what a blob transfer delivered so far in
  `SYNK_DIR/blobs/partial/<hash>`, kept across sessions and restarts.
- The `want` body becomes a list of `{hash, offset}`: an offset of N asks
  the sender to send only the bytes from N on, because the receiver
  already holds the first N bytes.
- The sender streams the blob in one pass over its stored copy: it hashes
  every byte (so corrupt store content is still detected and removed) but
  writes to the connection only the bytes from the requested offset on.
  The `blob` message carries `offset`, and its `size` is the length of the
  body that follows, the remaining bytes.
- The receiver hashes the staged prefix and the arriving bytes into one
  running SHA-256 and renames the staged file into the store only when
  the whole blob's hash matches. Until then the library never sees any of
  it, exactly as before.
- A partial that isn't finished is swept with the store's garbage
  collection after the same grace period as unreferenced content. A
  partial whose final hash doesn't match is discarded and the next
  transfer starts over.

Why not block splitting (like packages): the machinery exists for
packages, but applying it to plain files would put hundreds of part
hashes into every index for every large file, and would touch the scanner,
reconcile, apply, versions, and restore. Offset resume is contained to the
transfer layer, works for any blob (files, manifests, package parts), and
keeps indexes small.

## Consequences

- synk/2 changes (the `want` body's shape, a new `blob` field). It hasn't
  shipped, so it changes under the same name; [docs/PROTOCOL.md](../PROTOCOL.md)
  is updated with it.
- A peer can now leave staged bytes behind that survive between sessions.
  They are bounded by the same free-space check as complete blobs and are
  removed by the same sweep; a partial never reaches a library path or an
  index, so a bad or hostile transfer gains nothing over today.
- Recovery from an interrupted apply was already journal-based
  (`apply.Recover`); with this, an interruption during either the
  transfer or the apply of a large file loses at most the interrupted
  round's remaining work, never the user's data.
