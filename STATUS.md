# Status

- **Current phase:** finishing Phases 0 to 2 (see [ROADMAP.md](ROADMAP.md)).
- **Last done:** Phase 2's exit criterion demonstrated: resumable blob transfers (decision 0004), and tests and benchmarks in `internal/engine/scale_test.go` for all four lines — a real 4 GB file synced and resumed from its interrupted GiB; a warm scan of a 19.5 GB, 200,000-file library took 2.5 s and reads no content; a 256 MiB file moved with 1 MiB of peak memory rise; SYNK_DIR bounded by unique content, kept versions, and ~1 KiB per file.
- **Next step:** run `make check` and push this work, then the relay/worker npm vulnerabilities, then Phase 3.
- **Blocked on:** nothing.

## Needs the Maintainer

- Code scanning: the repo has GitHub's *default setup* for CodeQL enabled, so the advanced workflow in `.github/workflows/codeql.yml` runs but GitHub rejects its SARIF ("CodeQL analyses from advanced configurations cannot be processed when the default setup is enabled") and the run goes red. Either turn the default setup off in Settings → Code security and analysis (the workflow then uploads), or delete `codeql.yml` and keep the default setup. Until then, findings are triaged by running CodeQL locally; see docs/decisions/0002-codeql-triage.md. Local CodeQL attempts on 2026-11-16 failed for tooling reasons (query pack path, then "no Go code found" in the database) and were not pursued.

## Log

- 2026-11-16: Phase 2 benchmarks and scale tests landed. Resumable blob transfers: the `want` list and `blob` message carry offsets, staged partials live under `SYNK_DIR/blobs/partial/`, the whole blob's hash is verified before anything is stored (decision 0004, PROTOCOL.md updated). Measured on this machine: 4 GiB file synced in 61.7 s (66 MiB/s) and resumed from the interrupted GiB; idle warm scan of 19.5 GB across 200,000 files in 2.5 s with zero content reads (TestWarmScanReadsNoContent plants a wrong hash to prove it); 256 MiB file transfer raised peak memory by 1 MiB (TestMemoryDoesNotScaleWithFileSize); SYNK_DIR overhead bounded in TestSynkDirOverheadIsBounded.
- 2026-11-16: confirmed all three macOS-specific fixes from Phase 0 on a real Mac (iCloud dataless files, APFS case handling, privacy denials) and wrote docs/MACOS_CHECKS.md.
- 2026-11-16: fixed fold-aware case-collision data loss (decision 0003); synced the Makefile's staticcheck pin and go.mod's toolchain with CI (govulncheck clean); rewrote docs/THREAT_MODEL.md around the four adversaries Phase 1 named.
- 2026-11-16: repository prepared for open source.
