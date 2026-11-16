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

The build is ad-hoc signed (`CODE_SIGN_IDENTITY: "-"`), so it runs on the machine that built it. A distributable Developer ID build needs the maintainer's Apple Developer account — see STATUS.md, "Needs the Maintainer".

## iOS

1. Add an iOS App **Synk** target (`apps/ios/Synk/`) and a File Provider extension target (`apps/ios/Synk/FileProvider/`) to `project.yml` when Phase 5 starts; the shared client already compiles for iOS.
2. The long-term engine on iOS is the same Go code compiled with [gomobile](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile) into an `xcframework`. Until that target is wired, the iOS app uses the JSON protocol in `apps/shared/SynkClient.swift` against a paired Mac on the LAN.

### gomobile (later)

```bash
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile bind -target=ios -o dist/SynkCore.xcframework github.com/Zyniker13/synk/internal/engine
```

Export a narrower C API before binding; `internal/engine.Node` is the surface.

## Signing

Apple File Provider and iOS background tasks need a paid developer account. The sources are written so the first TestFlight can be "pair to a Mac, open Files → Synk, open that folder in Obsidian."
