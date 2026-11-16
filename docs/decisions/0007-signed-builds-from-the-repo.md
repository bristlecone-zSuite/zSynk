# 0007: The repo produces signed builds; the identity stays out

Date: 2026-11-16

## Context

Phase 4's done-line says "a signed build can be produced from the
repository." An app bundle is not validly signed when a build phase
copies an unsigned engine binary into it after the signature was made:
the seal covers the bundle contents, so the daemon copy invalidates it.
Distributing such a build fails Gatekeeper assessment for a reason that
is invisible at build time. Also, the signing identity (a Developer ID
Application certificate) is personal to the maintainer's Apple
Developer account and must never be committed.

## Decision

- The build's post-phase bundles the engine and, whenever code signing
  is enabled, signs the daemon with the hardened-runtime option and
  re-signs the app, so the bundle's seals are valid in every signed
  configuration. With `CODE_SIGNING_ALLOWED=NO` (tests) only the copy
  happens.
- `make release` builds a Release configuration and then verifies the
  result with `codesign --verify --deep --strict`. The signing identity
  comes from `SYNK_CODE_SIGN_IDENTITY`; empty means ad-hoc (`-`), which
  is what CI runs. The same machinery is exercised either way.
- A Go binary needs no extra entitlements under the hardened runtime:
  this was verified by signing `bin/synk` with `--options runtime` and
  serving from it. No entitlements file is shipped until a feature
  needs one (App Sandbox would be the first).
- Notarization is a maintainer step: it needs Apple account
  credentials, so the repo only carries the signed, hardened build and
  the docs say how to staple it.

## Consequences

- CI continuously proves the done-line's second half: `make release`
  runs ad-hoc on macOS and the seals are verified, so a future change
  that breaks the bundle's signature fails CI instead of shipping.
- A maintainer with a Developer ID certificate produces a distributable
  build with one command; nothing in the repo holds or references the
  certificate.
- The signature is only part of the story: an unnotarized download
  still needs a right-click open on first launch, and the docs say so.
