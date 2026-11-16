# Status

- **Current phase:** Phase 3 (see [ROADMAP.md](ROADMAP.md)).
- **Last done:** Phase 2 finished and pushed (resumable transfers, decision 0004, all four exit lines demonstrated); the six relay/worker npm vulnerabilities fixed by the wrangler upgrade; Phase 3's `pause`, `resume`, `remove`, `unpair` commands and the `GET /v1/events` event stream landed with tests (decision 0005).
- **Next step:** push Phase 3 once `make check` is green and CI passes, then Phase 4, the Mac app to 0.1.0.
- **Blocked on:** nothing.

## Needs the Maintainer

- Code scanning: the repo has GitHub's *default setup* for CodeQL enabled, so the advanced workflow in `.github/workflows/codeql.yml` runs but GitHub rejects its SARIF ("CodeQL analyses from advanced configurations cannot be processed when the default setup is enabled") and the run goes red. Either turn the default setup off in Settings → Code security and analysis (the workflow then uploads), or delete `codeql.yml` and keep the default setup. Until then, findings are triaged by running CodeQL locally; see docs/decisions/0002-codeql-triage.md. Local CodeQL attempts on 2026-11-16 failed for tooling reasons (query pack path, then "no Go code found" in the database) and were not pursued.
- Dependabot's vulnerability banner on the repo page may still list the six old findings until GitHub re-scans; the pushed lockfile is clean (`npm audit` reports zero).

## Log

- 2026-11-16: Phase 3: `synk pause/resume/remove/unpair` with engine methods, API routes, client calls, and CLI (decision 0005 — removing keeps files, versions, and index); `GET /v1/events` streams sync failures, held deletes, and membership changes as server-sent events (engine pub/sub drops events for slow readers); journal compaction was already in place, M7 validation and the H10 daemon lock were verified as landed.
- 2026-11-16: relay/worker: upgraded wrangler 4.148.0 → 4.163.0 to clear its transitive sharp and undici advisories; `npm audit` clean and `npm run check` passes.
- 2026-11-16: Phase 2 benchmarks and scale tests landed. Resumable blob transfers: the `want` list and `blob` message carry offsets, staged partials live under `SYNK_DIR/blobs/partial/`, the whole blob's hash is verified before anything is stored (decision 0004, PROTOCOL.md updated). Measured on this machine: 4 GiB file synced in 61.7 s (66 MiB/s) and resumed from the interrupted GiB; idle warm scan of 19.5 GB across 200,000 files in 2.5 s with zero content reads (TestWarmScanReadsNoContent plants a wrong hash to prove it); 256 MiB file transfer raised peak memory by 1 MiB (TestMemoryDoesNotScaleWithFileSize); SYNK_DIR overhead bounded in TestSynkDirOverheadIsBounded.
- 2026-11-16: confirmed all three macOS-specific fixes from Phase 0 on a real Mac (iCloud dataless files, APFS case handling, privacy denials) and wrote docs/MACOS_CHECKS.md.
- 2026-11-16: fixed fold-aware case-collision data loss (decision 0003); synced the Makefile's staticcheck pin and go.mod's toolchain with CI (govulncheck clean); rewrote docs/THREAT_MODEL.md around the four adversaries Phase 1 named.
- 2026-11-16: repository prepared for open source.
