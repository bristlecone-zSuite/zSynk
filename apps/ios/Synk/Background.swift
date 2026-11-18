import BackgroundTasks
import os
import UIKit

/// iOS gives an app no steady background CPU. What it does give is
/// BGTaskScheduler: the system launches the app briefly at its own
/// discretion — a few opportunities a day, more when the user opens the
/// app often — and hands the task a deadline. Synk asks for two of
/// those: a quick refresh soon after backgrounding and a longer
/// processing pass that wants network. When the user leaves the app,
/// the app also gives itself the short background time iOS grants every
/// backgrounding to finish a sync pass of whatever changed just before.
/// None of it is guaranteed; the honest limits are in the README and
/// decision 0014.
enum BackgroundSync {
    static let refreshID = "com.zyniker.synk.refresh"
    static let processingID = "com.zyniker.synk.processing"

    /// The model the handlers work on. SynkModel sets this when it is
    /// created. A cold background launch starts without a scene having
    /// run, so the task body opens the engine itself if needed.
    static weak var model: SynkModel?

    /// Registers the two task handlers. This must run before the app
    /// finishes launching, so the app delegate calls it; registering an
    /// already-registered identifier returns false and leaves the first
    /// handler in place, which keeps test hosts and relaunches quiet.
    static func register() {
        let scheduler = BGTaskScheduler.shared
        scheduler.register(forTaskWithIdentifier: refreshID, using: nil) { task in
            runSync(task)
        }
        scheduler.register(forTaskWithIdentifier: processingID, using: nil) { task in
            runSync(task)
        }
    }

    /// Asks the system for the next chances to sync. Submissions can
    /// fail (a missing Info.plist identifier, too many pending requests,
    /// an unregistered identifier); background sync is best effort by
    /// nature, so the failure is quiet and the return value is for
    /// tests.
    @discardableResult
    static func submitRequests() -> Bool {
        let scheduler = BGTaskScheduler.shared

        let refresh = BGAppRefreshTaskRequest(identifier: refreshID)
        // Soon after backgrounding, while the user's context is fresh;
        // the system decides what "soon" really is.
        refresh.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60)

        let processing = BGProcessingTaskRequest(identifier: processingID)
        processing.requiresNetworkConnectivity = true
        processing.requiresExternalPower = false
        // A longer pass for catching up on bigger changes; the system
        // usually grants these overnight or while charging.
        processing.earliestBeginDate = Date(timeIntervalSinceNow: 4 * 60 * 60)

        var submitted = (try? scheduler.submit(refresh)) != nil
        submitted = (try? scheduler.submit(processing)) != nil && submitted
        return submitted
    }

    /// The task body: open the engine if this launch has no UI, run one
    /// sync pass, refresh the status, and tell the system it is done.
    /// The first of the task completing or the deadline firing finishes
    /// the task; the second call is a no-op.
    static func runSync(_ task: BGTask) {
        let completed = OSAllocatedUnfairLock(initialState: false)
        let finish: (Bool) -> Void = { ok in
            let first = completed.withLock { state -> Bool in
                let wasFirst = !state
                state = true
                return wasFirst
            }
            if first { task.setTaskCompleted(success: ok) }
        }
        let work = Task { @MainActor in
            guard let model else { finish(false); return }
            await model.ensureRunning()
            let ok = await model.runBackgroundPass()
            finish(ok)
        }
        task.expirationHandler = {
            work.cancel()
            finish(false)
        }
    }
}
