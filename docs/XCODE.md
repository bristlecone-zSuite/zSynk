# Opening the companion apps in Xcode

The Go daemon is the source of truth. The Swift targets are the thing a normal person launches.

## macOS

1. Create a new macOS App in Xcode named **Synk**, bundle id `com.zyniker.synk`.
2. Add every file under `apps/macos/Synk/` plus `apps/shared/`.
3. Enable:
   - App Sandbox: User Selected File (read/write), Incoming/Outgoing network
   - App Groups: `group.com.zyniker.synk` (optional, for a future helper)
4. Add a **Copy Files** build phase that places the `synk` binary (from `make build`) into the app bundle’s `Contents/MacOS/` next to the UI, or spawn `/usr/local/bin/synk` during development.
5. Menu bar: `LSUIElement` = `YES` if you want menu-bar-only; the sources support both a window and a menu extra.

The UI talks to `http://127.0.0.1:7421`. If the daemon is not running, the app starts it with `SYNK_DIR` under `~/Library/Application Support/Synk`.

## iOS

1. Create an iOS App **Synk**, bundle id `com.zyniker.synk.ios`.
2. Add files under `apps/ios/Synk/` and `apps/shared/`.
3. Add a File Provider extension target (`apps/ios/Synk/FileProvider/`) so libraries appear in Files.
4. Background Modes: background fetch, processing.
5. The long-term engine on iOS is the same Go code compiled with [gomobile](https://pkg.go.dev/golang.org/x/mobile/cmd/gomobile) into an `xcframework`. Until that target is wired, the iOS app uses the JSON protocol in `apps/shared/SynkClient.swift` against a paired Mac on the LAN.

### gomobile (later)

```bash
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile bind -target=ios -o dist/SynkCore.xcframework github.com/Zyniker13/synk/internal/engine
```

Export a narrower C API before binding; `internal/engine.Node` is the surface.

## Signing

Apple File Provider and iOS background tasks need a paid developer account. The sources are written so the first TestFlight can be “pair to a Mac, open Files → Synk, open that folder in Obsidian.”
