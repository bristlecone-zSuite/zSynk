# Status

- **Current phase:** finishing Phases 0 to 2 (see [ROADMAP.md](ROADMAP.md)).
- **Last done:** confirmed all three macOS-specific fixes from Phase 0 on a real Mac (iCloud dataless files, APFS case handling, privacy denials) and wrote docs/MACOS_CHECKS.md.
- **Next step:** the Phase 2 benchmarks (4 GB file sync + resume, idle scan bytes, memory vs file size, SYNK_DIR overhead), then the relay/worker npm vulnerabilities, then Phase 3.
- **Blocked on:** nothing.

## Needs the Maintainer

- Code scanning: the repo has GitHub's *default setup* for CodeQL enabled, so the advanced workflow in `.github/workflows/codeql.yml` runs but GitHub rejects its SARIF ("CodeQL analyses from advanced configurations cannot be processed when the default setup is enabled") and the run goes red. Either turn the default setup off in Settings → Code security and analysis (the workflow then uploads), or delete `codeql.yml` and keep the default setup. Until then, findings are triaged by running CodeQL locally; see docs/decisions/0002-codeql-triage.md.

## Log

- 2026-11-16: confirmed all three macOS-specific fixes from Phase 0 on a real Mac (iCloud dataless files, APFS case handling, privacy denials) and wrote docs/MACOS_CHECKS.md.
- 2026-11-16: fixed fold-aware case-collision data loss (decision 0003); synced the Makefile's staticcheck pin and go.mod's toolchain with CI (govulncheck clean); rewrote docs/THREAT_MODEL.md around the four adversaries Phase 1 named.
- 2026-11-16: repository prepared for open source.
