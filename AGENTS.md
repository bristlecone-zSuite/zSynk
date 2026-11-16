# Working in This Repository

This guide is for anyone, human or agent, changing this repository. Read [README.md](README.md), [ROADMAP.md](ROADMAP.md) and [STATUS.md](STATUS.md) first: they say what Synk is, what comes next, and where the work stands. [CONTRIBUTING.md](CONTRIBUTING.md) has the setup and pull request rules.

## Priorities

Data safety, then security, then correctness, then speed, then features. The five safety rules in the README are never traded for anything else.

## Rules

- Run `make check` before every push, and `npm run check` in `relay/worker` when the relay changes. CI runs the same commands on Linux and macOS.
- Every change in behavior comes with tests. Every bug fix starts with a regression test that fails without the fix.
- Never skip, disable, or loosen a test or a safety check to get CI green.
- A change to the wire format or pairing updates [docs/PROTOCOL.md](docs/PROTOCOL.md) and, where it changes what an attacker can do, [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md).
- Record each significant decision in [docs/decisions/](docs/decisions/) (Context, Decision, Consequences), numbered in order.
- Keep [STATUS.md](STATUS.md) current: current phase, last done, next step, blocked on, and a log line.
- Keep claims in the README and docs no stronger than the code and its tests. When a fix lands, update the doc that described the gap.
- Work on `main` in small commits that keep CI green, or on a short-lived branch for a large change. Never force-push or rewrite pushed history.
- Add dependencies only when the gain is clear, from well-known, maintained projects with permissive licenses, and record any security-relevant one in a decision record.
- Self-hosting is a requirement: everything Synk needs must run without any service of ours. A hosted relay may be offered for convenience, but a user must be able to run their own from this repository.

## Public Repository

Everything here is public, forever. Never commit secrets of any kind (keys, tokens, `.env` contents, device identity files), email addresses, hostnames, IP addresses, local paths outside the repository, or anything about the machines, services, or tools used to develop it. Test data uses obviously fake values (`alice`, `bob`, `example.com`, `Studio Mac`). Commit messages describe the code only.
