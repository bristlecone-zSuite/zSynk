# 0008: The mobile package embeds the engine for iOS

Date: 2026-11-16

## Context

Phase 5 needs the engine on iOS. There is no separate daemon process
there, so the embedding host owns the lifecycle, and gomobile's binding
only carries a narrow slice of Go's type system across to Swift.

## Decision

- The public `mobile` package wraps `engine.Node`. Its exported surface
  is only what gomobile can bind: methods on one pointer type. Boolean
  results cross as thrown errors; string results carry a JSON envelope
  (`{"ok":true,"data":…}` / `{"ok":false,"error":"…"}`) because a
  non-null string with an out error pointer cannot express "nil on
  error" safely across the bridge. The envelope's payloads use the same
  shapes the REST API serves, so the app-side clients decode types they
  already have.
- `mobile.Open` starts serving peer connections on an ephemeral port
  (`0.0.0.0:0`) and runs the watch-and-sync loop; `Close` stops both
  and releases the data folder. `engine.Close` only releases the lock,
  so the loop and listener are the package's to stop.
- The ephemeral port is deliberate: an iOS app's background relaunches
  would collide on a fixed one. Reconnection into the phone is not
  needed for sync — the phone dials out to the Mac's fixed listen port
  recorded when pairing — and the pairing link carries the actual
  addresses of the moment.
- `AddLibrary` mirrors `POST /v1/libraries`: the ID defaults to the
  preset's (so the same app syncs between devices) and an empty path
  falls back to the preset's default location.
- `StartPairing` reuses the API's offer flow; `PairWithMac` parses the
  scanned `synk://pair` link and tries every address in it, with the
  link's fingerprint pinning the offering device's key.

## Consequences

- The integration test `TestPairThenSyncThroughMobile` pairs two
  embedded nodes through a scanned link and syncs a file both ways with
  no HTTP API and no separate process — the shape the iOS app will run.
- The package cross-compiles for ios/arm64 (`CGO_ENABLED=0 GOOS=ios
  GOARCH=arm64 go build ./mobile/`), which CI can check cheaply before
  any gomobile binding is attempted.
- Events still cross by polling Status, not by subscription; a callback
  interface can be added when the iOS app needs live updates.
