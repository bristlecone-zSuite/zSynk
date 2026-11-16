# 0002: CodeQL findings are by design or already contained

**Status:** accepted, 2026-11-16.

## Context

Code scanning reports two findings on the Go engine. The advanced CodeQL workflow can't upload its SARIF while GitHub's default setup is enabled (a repository setting, not a file in the repository), so the findings come from the default-setup scan and can only be dismissed by someone with repository access. Until then, the triage lives here.

## Findings

### The certificate check (`internal/transport/tls.go`)

CodeQL sees `InsecureSkipVerify: true` and warns that certificate verification is disabled. That is the design, not a bug: Synk's devices authenticate each other by **pinning the peer's whole Ed25519 public key**, learned at pairing (or from the offer link's `fp` fingerprint during pairing). There is no certificate authority in the model, so the X.509 chain carries no trust. The pin check runs in `VerifyConnection`, after the handshake proves the peer holds the pinned key's private half, and the check fails the handshake before any application byte moves. `docs/PROTOCOL.md` documents this; the threat model's "attacker on the network path" section covers what it protects against.

A chain-validating configuration would be *less* safe here: it would make devices trust any certificate a network attacker crafts with a valid chain, unless the code also pinned keys — which is exactly what it does.

### Path injection (uncontrolled data used in path derivation)

Every path that reaches a file operation from outside the process is validated at the boundary where it enters, and every file operation on library content goes through an `os.Root` opened on the library, which refuses `..` and symlink escapes at the system-call level:

- Entries and package manifests from a peer pass `checkRemoteEntry` and `packages.Manifest.Validate`, which require `safepath.Check` (clean, relative, no `..`, outside `.synk`) and a well-formed SHA-256 for every hash.
- `internal/apply` re-checks every op path with `safepath.Check` and performs creates, renames, and removes through `os.Root`.
- The vault names version directories by hashing the relative path (`Vault.PathDir`), never by joining it.
- Quarantine names come from hashes validated by `blob.ValidateHash`, and the free-form label is reduced to `[A-Za-z0-9_-]` before `os.MkdirTemp`.
- Blob store paths are built only from validated 64-hex hashes.
- Library IDs are restricted to `[a-z0-9-]{1,63}` because they name files in the data folder.
- The local API accepts library paths only from a token-bearing loopback client, and `checkLibraryPath` refuses the home folder, cloud containers, Synk's data folder, and other libraries.

CodeQL's data-flow analysis can't model the token gate or the `os.Root` syscall semantics, so it flags derivation sites whose inputs are either validated at the boundary or controlled by the user running the CLI.

## Decision

Keep both designs. Do not weaken the pin check to satisfy the query, and do not add redundant path validation for its own sake. When the repository is switched from the default CodeQL setup to the advanced workflow, dismiss these findings with a reference to this record.

## Consequences

- The pinned-key model stays; `docs/PROTOCOL.md` and `docs/THREAT_MODEL.md` are the sources of truth for it.
- Any new code that takes a path from a peer, the API, or a manifest must go through `safepath.Check` and `os.Root`; the assessment's tests (`make assess` reproductions for C3) stay in the suite to keep that true.
