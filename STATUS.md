# Status

- **Current phase:** finishing Phases 0 to 2 (see [ROADMAP.md](ROADMAP.md)).
- **Last done:** fixed a real data-loss bug a device probe found: on a volume that folds case, a peer's second spelling of one name was deferred forever and its content never landed (README rule 5 violation). The engine now probes the volume, reconciles fold-colliding paths like a concurrent edit (winner at the path, loser as a conflict copy), and the apply backstop guards the classes the rewrite can miss. See docs/decisions/0003-fold-aware-case-collisions.md. The CodeQL triage is recorded in docs/decisions/0002-codeql-triage.md.
- **Next step:** the remaining Phase 0 macOS verification (docs/MACOS_CHECKS.md), then the Phase 2 benchmarks, then the relay/worker npm vulnerabilities, then Phase 3.
- **Blocked on:** nothing.

## Needs the Maintainer

- Code scanning: the repo has GitHub's *default setup* for CodeQL enabled, so the advanced workflow in `.github/workflows/codeql.yml` runs but GitHub rejects its SARIF ("CodeQL analyses from advanced configurations cannot be processed when the default setup is enabled") and the run goes red. Either turn the default setup off in Settings → Code security and analysis (the workflow then uploads), or delete `codeql.yml` and keep the default setup. Until then, findings are triaged by running CodeQL locally; see docs/decisions/0002-codeql-triage.md.

## Log

- 2026-11-16: fixed fold-aware case-collision data loss (decision 0003); synced the Makefile's staticcheck pin and go.mod's toolchain with CI (govulncheck clean); rewrote docs/THREAT_MODEL.md around the four adversaries Phase 1 named.
- 2026-11-16: repository prepared for open source.
