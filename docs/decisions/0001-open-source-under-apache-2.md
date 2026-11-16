# 0001: Open Source Under Apache-2.0

**Status:** accepted, 2026-11-16.

## Context

Synk asks people to trust it with their only copy of their documents. Sync tools earn that trust by being inspectable: people want to read the code that can delete their files, and run it without depending on a company's servers. The engine was already licensed Apache-2.0, with Syncthing (MPL-2.0) kept as a possible optional transport.

## Decision

Develop Synk in the open under the Apache License 2.0, with inbound contributions under the same license (section 5) and no contributor license agreement. Everything Synk needs runs without a service of ours: any hosted relay is optional, and a self-hostable relay lives in this repository.

## Consequences

- Anyone can read, audit, fork, and ship Synk, including in proprietary products, subject to the license's notice terms.
- Contributors keep their copyright; the NOTICE file and license headers stay accurate as the code grows.
- The repository must never hold secrets or private operating details, since every commit is public forever (see AGENTS.md).
- Security reports need a private channel (SECURITY.md), because the issue tracker is public.
