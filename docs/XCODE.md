# Opening the companion apps in Xcode

The Go daemon is the source of truth. The Swift targets are the thing a normal person launches.

## macOS

The Xcode project is generated from `project.yml` with [XcodeGen](https://github.com/yonaskolb/XcodeGen) (both installed with `brew install xcodegen`):

```bash
make build          # builds the engine the app carries: bin/synk
xcodegen generate   # writes Synk.xcodeproj (git-ignored; don't edit it)
open Synk.xcodeproj
```

`make app` builds the app; `make app-test` runs its tests, which drive **two real daemons** on temporary data folders and pair them through the app's client — the same story the product tells. CI runs `make app-test` on macOS.

Layout:

- `apps/macos/Synk/` — the SwiftUI app: `SynkApp.swift` (scenes and model), `Views.swift`, `Daemon.swift` (starts and follows the bundled daemon).
- `apps/shared/SynkClient.swift` — the API client every app uses: token-authenticated, typed, with the event-stream parser and the `synk://pair` link parser.
- `apps/tests/` — the integration tests above.

The app bundle carries the engine at `Contents/MacOS/synk-daemon` (copied from `bin/synk` by a build phase). It is deliberately **not** named `synk`: on the case-insensitive default volume that name overwrites the app binary itself.

The app talks to `http://127.0.0.1:7421` with the bearer token in the data folder (`~/Library/Application Support/Synk/api-token`). If no daemon is running, the app starts the one it carries with `SYNK_DIR` there; a daemon started by hand is reused.

The build is ad-hoc signed (`CODE_SIGN_IDENTITY: "-"`), so it runs on the machine that built it. The bundled engine is signed and the app re-signed after the copy, so the bundle's seals are valid in every signed configuration (decision 0007).

## iOS

The iOS app target **Synk-iOS** lives in `project.yml`: sources in
`apps/ios/Synk/` plus the shared client, with the engine embedded as a
gomobile framework. It pairs by scanning or pasting a `synk://pair`
link, adds libraries from the catalog into its own Documents folder,
and syncs while the app is open.

1. `make ios-framework` binds the `mobile` package into
   `apps/ios/Frameworks/SynkCore.xcframework` — a build product,
   git-ignored, pinned to the x/mobile version in go.mod.
2. `make ios-build` builds the app for the simulator.
3. `make ios-test` runs the iOS tests on a simulator: two embedded
   engines pair through a link and sync files both ways, through the
   same bridge calls the app drives.

Libraries live in the app's Documents folder, one per app preset, and
the Info.plist (`apps/ios/Synk/Info.plist`) carries
`UIFileSharingEnabled` and `LSSupportsOpeningDocumentsInPlace`, which
is what makes Documents appear as Files → On My iPhone → Synk
(decision 0010). The Info.plist is an explicit file rather than
`INFOPLIST_KEY_` build settings, because Xcode silently drops
generated-plist keys it does not know — `UIFileSharingEnabled` was lost
exactly that way once.

A File Provider extension target (`apps/ios/Synk/FileProvider/`) is the
planned next step in Phase 5.

### gomobile

The engine reaches Swift through the `mobile` package: a narrow, bindable API where complex values cross as JSON strings with the same shapes the REST API serves, and an opened node serves peers and runs the sync loop itself (decision 0008). Build it with `make ios-framework`; by hand, the same bind is:

```bash
gomobile bind -target=ios -o dist/SynkCore.xcframework github.com/bristlecone-zSuite/zSynk/mobile
```

## Signing

A distributable build is produced from the repository with:

```bash
make release SYNK_CODE_SIGN_IDENTITY="Developer ID Application: Name (TEAMID)"
```

The target builds a Release configuration, signs the bundled engine, re-signs the app, and verifies every seal with `codesign --verify --deep --strict`. Without `SYNK_CODE_SIGN_IDENTITY` the same machinery runs ad-hoc (that is what CI runs), which produces a build that runs only on the machine that made it.

The Developer ID Application certificate is personal to the maintainer's Apple Developer account and is never committed; nothing here needs it to build, test, or verify. For a download others can double-click, the signed app also has to be notarized — a maintainer step, since it needs Apple account credentials:

```bash
ditto -c -k --sequesterRsrc --keepParent Synk.app Synk.zip
xcrun notarytool submit Synk.zip --wait          # Apple account credentials
xcrun stapler staple Synk.app
```

An unnotarized Developer ID app opens with a right-click → Open on first launch; notarization removes that step.
