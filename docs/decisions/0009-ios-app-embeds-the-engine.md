# 0009: The iOS app embeds the engine through a gomobile framework

Date: 2026-11-16

## Context

The iOS app is the second user-facing surface. On the Mac, the app
launches the bundled daemon; iOS has no separate daemon process, and
its background rules make a fixed listening port a liability. The
engine reaches Swift through the `mobile` package, which needs a
compiled framework, and compiling one introduces the first runtime
dependency on code outside the Go standard library.

## Decision

- The iOS app links the engine as `SynkCore.xcframework`, generated at
  build time by `make ios-framework` (`gomobile bind` on the `mobile`
  package, pinned to the `golang.org/x/mobile` version in `go.mod`).
  The framework is a build product: it is git-ignored and every iOS
  build regenerates it, so no binary blob is ever committed.
- `golang.org/x/mobile` (the Go project's official mobile binding
  toolchain, BSD-3) is the first runtime-code dependency outside the
  standard library. It is recorded here because it is security
  relevant: its bindings sit between the engine and the app's process.
- The app opens the engine in its own Application Support folder with
  `mobile.Open`, which serves peers on an ephemeral port and runs the
  sync loop in-process. Sync goes out to the Mac's recorded address;
  nothing on the LAN needs to dial into the phone.
- The static mockup that came with the first public source is replaced
  by the real app: status from the engine, pairing by pasting a
  scanned `synk://pair` link. Camera scanning and library folders land
  with the steps that need them.
- CI builds the iOS app for the simulator (`make ios-build`) after the
  macOS app tests, so the embedded-engine app is continuously built;
  running it against a real Mac needs real hardware.

## Consequences

- A new `Synk-iOS` target in `project.yml` shares `apps/shared` with
  the Mac app; the File Provider stub moved out of the app's sources to
  `apps/ios/FileProvider` for the extension target that will consume it.
- The binding's shape rules the app's error handling: string results
  arrive as envelopes (decision 0008) and unwrap through one helper.
- `make ios-build` depends on the Go toolchain and the Xcode SDK being
  present, which CI's macOS job already provides.
