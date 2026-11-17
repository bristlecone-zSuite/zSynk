import XCTest
@testable import Synk
import SynkCore

/// The end-to-end story the iPhone app depends on, exercised on the iOS
/// runtime: two embedded engines pair through a scanned link, add their
/// libraries in the sandbox, and sync files both ways through the bridge
/// calls the app drives.
final class SyncBridgeTests: XCTestCase {
    private var engines: [MobileSynk] = []

    override func tearDown() {
        for engine in engines {
            _ = try? engine.close()
        }
        engines.removeAll()
        super.tearDown()
    }

    func testPairAndSyncBothWaysBetweenTwoEngines() throws {
        let mac = try openEngine(named: "Mac")
        let phone = try openEngine(named: "iPhone")

        // The Mac arms an offer; the phone joins through its link, the
        // way a scanned or pasted QR enters the app.
        let offer: PairOffer = try decodeBridge(mac.startPairing())
        try phone.pair(withMac: offer.qr)

        let phoneStatus: SynkStatus = try decodeBridge(phone.status())
        XCTAssertEqual(phoneStatus.peers.map(\.deviceId), [offer.deviceId],
                       "the phone doesn't see exactly the Mac it paired with")
        let macStatus: SynkStatus = try decodeBridge(mac.status())
        XCTAssertEqual(macStatus.peers.map(\.deviceId), [phoneStatus.deviceId],
                       "the Mac doesn't see exactly the phone that joined")

        // A library on each side, in temporary folders only.
        let macFolder = try directory(named: "vault-mac")
        let phoneFolder = try directory(named: "vault-phone")
        let macLibrary: SynkLibrary = try decodeBridge(mac.addLibrary("obsidian", label: "", path: macFolder.path))
        XCTAssertEqual(macLibrary.path, macFolder.path)
        _ = try decodeBridge(phone.addLibrary("obsidian", label: "", path: phoneFolder.path)) as SynkLibrary

        // Mac writes, phone pulls through the app's Sync now call.
        try Data("# from the Mac\n".utf8).write(to: macFolder.appendingPathComponent("note.md"))
        try phone.syncNow()
        XCTAssertEqual(try String(contentsOf: phoneFolder.appendingPathComponent("note.md"), encoding: .utf8),
                       "# from the Mac\n")

        // Phone writes, Mac pulls.
        try Data("from the phone\n".utf8).write(to: phoneFolder.appendingPathComponent("reply.md"))
        try mac.syncNow()
        XCTAssertEqual(try String(contentsOf: macFolder.appendingPathComponent("reply.md"), encoding: .utf8),
                       "from the phone\n")
    }

    // MARK: Helpers

    private func openEngine(named name: String) throws -> MobileSynk {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("synk-test-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        var openError: NSError?
        guard let engine = MobileOpen(dir.path, name, &openError) else {
            throw openError ?? NSError(domain: "SynkTests", code: 1,
                                       userInfo: [NSLocalizedDescriptionKey: "the engine didn't open"])
        }
        engines.append(engine)
        return engine
    }

    private func directory(named name: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("synk-files-\(UUID().uuidString)-\(name)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }
}
