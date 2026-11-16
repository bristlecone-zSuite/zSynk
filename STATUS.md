# Status

- **Current phase:** finishing Phases 0 to 2 (see [ROADMAP.md](ROADMAP.md)).
- **Last done:** synced the Makefile's staticcheck pin and go.mod's toolchain with CI (govulncheck now clean), and rewrote docs/THREAT_MODEL.md around the four adversaries Phase 1 named.
- **Next step:** triage the CodeQL findings (path injection and the pinned-key certificate check), then the Phase 0 macOS verification on this Mac, then the Phase 2 benchmarks.
- **Blocked on:** nothing.

## Needs the Maintainer

- Code scanning: the repo has GitHub's *default setup* for CodeQL enabled, so the advanced workflow in `.github/workflows/codeql.yml` runs but GitHub rejects its SARIF ("CodeQL analyses from advanced configurations cannot be processed when the default setup is enabled") and the run goes red. Either turn the default setup off in Settings → Code security and analysis (the workflow then uploads), or delete `codeql.yml` and keep the default setup. Until then, findings are triaged by running CodeQL locally; see docs/decisions/.

## Log

- 2026-11-16: repository prepared for open source.
