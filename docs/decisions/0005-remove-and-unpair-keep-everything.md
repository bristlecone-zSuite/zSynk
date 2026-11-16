# 0005: Removing a library or a device keeps everything

Date: 2026-11-16

## Context

Phase 3 adds the `pause`, `resume`, `remove`, and `unpair` commands. The
question for `remove` and `unpair` is what they delete. The data-safety
rules say a person's only copy is never at risk from a command whose
name doesn't say "delete", and there is no `synk delete` command at all.

## Decision

Neither command deletes anything:

- `remove --library ID` forgets a library: it stops syncing, leaves the
  config, and keeps the library's files, its `.synk` marker, the
  versions in the vault, and the index and scan cache on disk. Enabling
  the same folder again (with the same ID) resumes from the kept index,
  so nothing is reported as deleted and no peer re-sends anything.
- `unpair --device ID` forgets a paired device: every library stops
  sharing with it, its address book entry goes, and its connections are
  refused on both directions. Files synced before the unpair stay where
  they are on both devices, and the vault keeps its versions.

A paused library is likewise untouched: peers are refused while it is
paused, and everything that changed while it was paused syncs at the
next session after `resume`.

The event stream (`GET /v1/events`) publishes these changes and every
sync failure as server-sent events, so the apps can show them as they
happen; a subscriber that falls behind loses events rather than slowing
the daemon, because what happened is also in `synk.log` and status.

## Consequences

- Removing a library doesn't reclaim the disk its blobs and versions
  hold. They are freed by the same garbage collection as any
  unreferenced content, after the grace period, except the vault's
  retained versions, which stay until their own retention prunes them.
- One-sided unpair leaves the other device still trusting this one; its
  sessions are refused here, which is visible in its status and events.
  Pairing again after an unpair is a normal pairing with a fresh offer.
