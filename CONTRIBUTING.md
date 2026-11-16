# Contributing to Synk

Thanks for helping. Synk moves other people's documents between their devices, so the bar for a change is that it can't lose or leak them. Read the five safety rules in the [README](README.md#safety-rules-non-negotiable) before you start; a change that breaks one won't be merged, however useful it is.

## Before You Start

- For anything bigger than a small fix, open an issue first and say what you want to change and why. That saves you writing code we can't take.
- Security problems go through [SECURITY.md](SECURITY.md), never a public issue.
- Never attach real documents, libraries, `SYNK_DIR` contents, device keys, or logs with file names you'd rather keep private. Reproduce with a small made-up library instead.

## Setting Up

You need Go (the toolchain in `go.mod` downloads itself) and, for the relay, Node.js LTS.

```bash
git clone https://github.com/bristlecone-zSuite/zSynk
cd zSynk
make check            # go vet, the race detector, gofmt, staticcheck and govulncheck
make build            # bin/synk
cd relay/worker && npm ci && npm run check
```

The Swift apps in `apps/` are opened in Xcode as [docs/XCODE.md](docs/XCODE.md) describes.

## Making a Change

- **Tests come with every change in behavior.** A bug fix starts with a test that fails without the fix. Data-safety and security fixes need a test that shows the bad outcome no longer happens, not only that the new code runs.
- **Never weaken a safety check to get a test green.** If a test is wrong, say why in the pull request.
- **Keep the engine dependency-free** unless there's a strong reason. A new dependency needs a sentence in the pull request on why it's worth it and how well it's maintained.
- **Protocol changes** update [docs/PROTOCOL.md](docs/PROTOCOL.md) in the same pull request. Once synk/2 ships, a change an older device can't read needs a new protocol name.
- **Significant decisions** get a short record in [docs/decisions/](docs/decisions/) (Context, Decision, Consequences).
- Match the style of the code around you, run `gofmt`, and keep comments about why, not what.

## Pull Requests

- Keep each pull request to one change, with a title that says what it does.
- Say what you changed, why, and what you ran (`make check` at least).
- CI must pass: Linux and macOS tests, the relay check, govulncheck and CodeQL.

## License

Synk is licensed under the [Apache License 2.0](LICENSE). By submitting a contribution, you agree that it's licensed under the same terms, as section 5 of the license says. Only submit work you have the right to license that way.
