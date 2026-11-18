import XCTest
@testable import Synk
import SynkCore
import FileProvider

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

    /// The File Provider plumbing (decision 0011): the app group
    /// container holds the synced libraries, the model snapshots them,
    /// and the system accepts the domain registration and the
    /// working-set signal the app sends — the extension is embedded and
    /// reachable.
    func testFileProviderDomainAndSignalAreAcceptedByTheSystem() throws {
        guard let group = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: FileProviderModel.appGroup) else {
            throw XCTSkip("the app group container is unavailable")
        }
        let docs = group.appendingPathComponent("Documents", isDirectory: true)
        let library = docs.appendingPathComponent("FP Test \(UUID().uuidString.prefix(8))", isDirectory: true)
        try FileManager.default.createDirectory(at: library, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: library) }
        try Data("synced\n".utf8).write(to: library.appendingPathComponent("note.md"))

        let snapshot = FileProviderModel.snapshot(librariesRoot: docs)
        XCTAssertTrue(snapshot.contains { $0.id == "synk/\(FileProviderModel.encode(library.lastPathComponent))" },
                      "the fixture library isn't in the snapshot: \(snapshot.map(\.id))")

        // Register the domain. Re-registering an existing domain is
        // expected (the disk location already exists), so only a real
        // error is a failure.
        var domainError: Error?
        var domainDone = false
        NSFileProviderManager.add(
            NSFileProviderDomain(identifier: FileProviderModel.domain, displayName: "Synk")) { err in
            domainError = err
            domainDone = true
        }
        let domainDeadline = Date().addingTimeInterval(15)
        while !domainDone && Date() < domainDeadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        XCTAssertTrue(domainDone, "the system never answered the domain registration")
        if let err = domainError as? NSError,
           err.domain == NSCocoaErrorDomain, err.code == 516 { // NSFileWriteFileExistsError
            // Expected on a re-run; the domain is already registered.
        } else {
            XCTAssertNil(domainError, "the system rejected the domain: \(domainError?.localizedDescription ?? "")")
        }

        // Signal the working set so the system pulls changes from the
        // extension's enumerator.
        var signalError: Error?
        var signalDone = false
        NSFileProviderManager.default.signalEnumerator(for: .workingSet) { err in
            signalError = err
            signalDone = true
        }
        let signalDeadline = Date().addingTimeInterval(15)
        while !signalDone && Date() < signalDeadline {
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        XCTAssertTrue(signalDone, "the system never answered the working-set signal")
        XCTAssertNil(signalError, "the system rejected the signal: \(signalError?.localizedDescription ?? "")")
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
