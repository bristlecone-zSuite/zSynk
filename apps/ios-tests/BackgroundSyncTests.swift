import XCTest
@testable import Synk

/// The background scheduling contract (decision 0014): the identifiers
/// the app registers are exactly the ones its Info.plist declares, and
/// submitting the requests succeeds in the host app, where registration
/// happened at launch. The system's own decision of when to actually
/// fire a task cannot be tested here — those limits are documented, not
/// promised.
///
/// The submit assertion is gated off the simulator (decision 0017): the
/// simulator has no background-task daemon, so BGTaskScheduler cannot accept
/// a submission there. The identifier contract is still enforced on every
/// platform by testRegisteredIdentifiersMatchTheInfoPlist, and the submit
/// path is exercised in full on a real device.
///
/// The tests rely on the host app's launch-time registration and never
/// call register() themselves: a second registration in the same process
/// is an NSInternalInconsistencyException, not a no-op.
final class BackgroundSyncTests: XCTestCase {
    func testRegisteredIdentifiersMatchTheInfoPlist() {
        let declared = Bundle.main.object(
            forInfoDictionaryKey: "BGTaskSchedulerPermittedIdentifiers"
        ) as? [String]
        XCTAssertEqual(
            Set(declared ?? []),
            Set([BackgroundSync.refreshID, BackgroundSync.processingID]),
            """
            registers a task the Info.plist does not permit \
            (or the other way round): \(declared ?? [])
            """
        )
    }

    func testSubmittingBackgroundSyncRequestsSucceeds() throws {
        // The host app registered the launch handlers when it launched, and
        // the test must not register them again: BGTaskScheduler allows
        // exactly one handler per identifier per process, and a second
        // registration raises NSInternalInconsistencyException rather than
        // being a no-op.
        #if targetEnvironment(simulator)
        // The simulator has no background-task daemon, so BGTaskScheduler
        // cannot accept a submission there (decision 0017). The identifier
        // contract is still enforced by
        // testRegisteredIdentifiersMatchTheInfoPlist, and the submit path is
        // exercised in full on a real device.
        throw XCTSkip(
            "BGTaskScheduler submission is not supported on the simulator"
        )
        #endif
        XCTAssertTrue(
            BackgroundSync.submitRequests(),
            """
            BGTaskScheduler rejected the requests — is \
            BGTaskSchedulerPermittedIdentifiers in the host app's Info.plist?
            """
        )
    }
}
