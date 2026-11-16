# Security Policy

Synk syncs private documents between devices, so we treat security reports as the most urgent kind of bug.

## Reporting a Vulnerability

Report it privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Please don't open a public issue, pull request, or discussion about it.

Include what you found, the version or commit, and the steps to reproduce it. Use a made-up library and devices; never send real documents or keys.

We'll acknowledge a report within a few days, keep you updated as we fix it, and credit you in the advisory unless you'd rather we didn't.

## Supported Versions

Synk hasn't had a release yet. Fixes go to `main`; once releases start, the latest release gets security fixes.

## Scope

The threat model is in [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) and the peer protocol in [docs/PROTOCOL.md](docs/PROTOCOL.md). Things we especially want to hear about:

- anything that deletes, overwrites, or corrupts a file without keeping a recoverable copy;
- a peer, a relay, a web page, or another local user reading or changing synced data;
- a paired device writing outside the libraries it was given;
- pairing that can be completed or hijacked without the code, link, or QR.
