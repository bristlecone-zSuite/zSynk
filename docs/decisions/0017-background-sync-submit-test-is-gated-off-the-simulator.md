# 0017 — The background-sync submit test is gated off the simulator

## Context

`BackgroundSyncTests` has two tests. The first,
`testRegisteredIdentifiersMatchTheInfoPlist`, checks that the identifiers the
app registers are exactly the ones its Info.plist declares, and it passes
everywhere. The second, `testSubmittingBackgroundSyncRequestsSucceeds`,
asserts that `BGTaskScheduler.shared.submit` accepts both of the app's
requests.

On the CI simulator the second test fails: `submit` returns an error. The
Info.plist is correct (the first test passes) and the app delegate registers
the handlers at launch, so the rejection is not a missing-identifier or
missing-registration bug. The cause is the platform: the iOS Simulator has no
background-task daemon, so `BGTaskScheduler.submit` cannot be accepted there.
This is a long-standing simulator limitation, not a regression.

The first attempt at this fix also called `BackgroundSync.register()` at the
start of the submit test, on the assumption that registering an
already-registered identifier is a no-op. CI disproved that: the host app's
app delegate registers the handlers at launch, and the test's second
registration crashed with `NSInternalInconsistencyException` — "Launch
handler for task with identifier com.zyniker.synk.refresh has already been
registered". `BGTaskScheduler.register` allows exactly one handler per
identifier per process and is not idempotent.

## Decision

The submit assertion is gated off the simulator with
`#if targetEnvironment(simulator)`: on the simulator the test skips with a
clear reason, and on a real device it runs in full. The test never calls
`BackgroundSync.register()` itself: registration is the host app's job, and
doing it again from the test raises `NSInternalInconsistencyException`.

This is not a loosened check. The assertion itself is unchanged, and the
identifier contract — the part that actually protects the app from silently
failing to schedule background sync — is still enforced by the first test,
which runs on every platform. The gate follows the convention the same file
already uses: the File Provider test skips when the app-group container is
unavailable on the device.

## Consequences

- CI is green on the simulator, and the submit path is still verified on a
  real device.
- If a future change breaks registration or the Info.plist identifiers, the
  first test catches it on every platform, including the simulator.
- The skip reason is explicit, so a reader knows exactly what is not covered
  on the simulator and where it is covered instead.
