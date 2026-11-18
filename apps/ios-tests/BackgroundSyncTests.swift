import XCTest
@testable import Synk

/// The background scheduling contract (decision 0014): the identifiers
/// the app registers are exactly the ones its Info.plist declares, and
/// submitting the requests succeeds in the host app, where registration
/// happened at launch. The system's own decision of when to actually
/// fire a task cannot be tested here — those limits are documented, not
/// promised.
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

    func testSubmittingBackgroundSyncRequestsSucceeds() {
        XCTAssertTrue(
            BackgroundSync.submitRequests(),
            """
            BGTaskScheduler rejected the requests — is \
            BGTaskSchedulerPermittedIdentifiers in the host app's Info.plist?
            """
        )
    }
}
