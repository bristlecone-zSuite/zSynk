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
        // Guarantee the identifiers are registered, independently of whether
        // the host app's launch path ran. Registering an already-registered
        // identifier is a no-op that keeps the first handler.
        BackgroundSync.register()
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
