# Decision 14: iOS background sync is opportunistic

Date: 2026-11-17

## Context

A phone is the most likely device to hold a person's only copy of their
documents, and it is the device that changes them (photos, notes made on the
move). On iOS an app gets no steady background CPU: once the user leaves the
app the system suspends it within seconds, and the engine's watch loop is
frozen. Without anything else, a change made on the phone would only leave
the phone the next time the user opens the app.

What iOS offers instead is BGTaskScheduler: the app asks the system for work
and the system launches the app briefly at its own discretion. There are two
kinds relevant here — an app refresh task (short, opportunistic, the system
learns when to fire it from how the user uses the app) and a processing task
(longer, may be granted overnight or while charging). The system guarantees
neither; some days may pass without a launch, and the app gets a deadline of
tens of seconds once it is running.

Push notifications from the Mac would make background arrival instant, but
they require a server that holds the user's device tokens — which Synk will
not have by design (self-hosting is a requirement, decision 0002).

## Decision

Synk on iOS syncs in the background through three mechanisms, all best
effort:

1. **On backgrounding**, the app submits one refresh request (asking for a
   chance within about fifteen minutes) and one processing request (asking
   for a chance within about four hours, requiring network), and — with the
   short background time iOS grants every backgrounding — finishes one sync
   pass of whatever changed just before.
2. **A refresh task**, when the system grants one, opens the engine if the
   launch has no UI, runs one sync pass, refreshes the status, and
   completes.
3. **A processing task**, when the system grants one, does the same with a
   longer horizon.

The identifiers are `com.zyniker.synk.refresh` and
`com.zyniker.synk.processing`, registered at launch through the app
delegate (BGTaskScheduler requires registration before the app finishes
launching, which no SwiftUI code can reach).

## Consequences

- The iPhone→Mac direction of a change leaves the phone within seconds of
  the user leaving the app (the flush), and Mac→iPhone arrives when the
  system next grants a task — minutes to days, not guaranteed.
- The Files view (decision 0011) reflects the on-disk library folder; it
  shows background-arrived files whenever the engine happens to have run,
  and the enumerator's staleness handling covers the rest.
- Documentation (README) must keep the honest phrasing: background sync is
  best effort on iOS; no push notifications, because that would require a
  service of ours.
- A test pins the identifiers to the Info.plist and proves submissions are
  accepted in the host app. When the system actually fires a task is not
  unit-testable; it is documented, not promised.
