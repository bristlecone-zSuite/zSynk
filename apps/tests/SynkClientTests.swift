import XCTest
@testable import Synk

/// Integration tests: they run the real `synk` daemon twice, pair the two
/// through the app's client, and sync files between them — the story the
/// product tells, exercised end to end.
final class SynkClientTests: XCTestCase {
    override func setUpWithError() throws {
        let daemon = DaemonFixture.daemonPath
        try XCTSkipUnless(FileManager.default.isExecutableFile(atPath: daemon), "run make build so bin/synk exists")
    }

    /// Pairing through a synk://pair link syncs files both ways, keeps a
    /// version when content changes, and restores it.
    func testPairAndSyncBetweenTwoDaemons() async throws {
        let fixture = try await DaemonFixture(two: true)
        defer { fixture.stop() }

        // Pair: A makes an offer; B reads the QR link from it.
        let offer = try await fixture.clientA.startPair()
        let link = PairLink(link: offer.qr)
        XCTAssertNotNil(link, "the offer's QR isn't a synk://pair link: \(offer.qr)")
        XCTAssertEqual(link?.code, offer.code)
        try await fixture.clientB.joinPair(link!)

        let statusA = try await fixture.clientA.status()
        XCTAssertEqual(statusA.peers.count, 1, "A doesn't see B after joining")

        // Share a folder both ways.
        let dirA = fixture.temporaryDirectory("notes-A")
        let dirB = fixture.temporaryDirectory("notes-B")
        _ = try await fixture.clientA.enable(preset: "custom", path: dirA.path, label: "Notes", id: "notes")
        _ = try await fixture.clientB.enable(preset: "custom", path: dirB.path, label: "Notes", id: "notes")

        // A writes, syncs, B has it.
        try "hello from A".write(to: dirA.appendingPathComponent("a.md"), atomically: true, encoding: .utf8)
        try await fixture.clientA.sync()
        try await waitUntil("b.md's content to arrive at B") {
            (try? String(contentsOf: dirB.appendingPathComponent("a.md"), encoding: .utf8)) == "hello from A"
        }

        // B writes back, syncs, A has it.
        try "hello from B".write(to: dirB.appendingPathComponent("b.md"), atomically: true, encoding: .utf8)
        try await fixture.clientB.sync()
        try await waitUntil("B's file to arrive at A") {
            (try? String(contentsOf: dirA.appendingPathComponent("b.md"), encoding: .utf8)) == "hello from B"
        }

        // An overwrite keeps the replaced content as a version, and the
        // app can put it back.
        try "second version".write(to: dirA.appendingPathComponent("a.md"), atomically: true, encoding: .utf8)
        try await fixture.clientA.sync()
        try await waitUntil("the overwrite to arrive at B") {
            (try? String(contentsOf: dirB.appendingPathComponent("a.md"), encoding: .utf8)) == "second version"
        }
        let versions = try await fixture.clientB.versions(libraryID: "notes", path: "a.md")
        let kept = versions.contains { $0.reason == "pre-overwrite" && $0.size == 12 }
        XCTAssertTrue(kept, "B kept no version of a.md: \(versions)")

        try await fixture.clientB.restore(libraryID: "notes", path: "a.md", version: versions[0].id)
        try await waitUntil("the restore to be seen at A") {
            (try? String(contentsOf: dirA.appendingPathComponent("a.md"), encoding: .utf8)) == "hello from A"
        }
    }

    /// Pausing a library stops it syncing; what changed while it was
    /// paused arrives after resuming.
    func testPauseStopsSyncingUntilResume() async throws {
        let fixture = try await DaemonFixture(two: true)
        defer { fixture.stop() }

        let offer = try await fixture.clientA.startPair()
        try await fixture.clientB.joinPair(PairLink(link: offer.qr)!)

        let dirA = fixture.temporaryDirectory("notes-A")
        let dirB = fixture.temporaryDirectory("notes-B")
        _ = try await fixture.clientA.enable(preset: "custom", path: dirA.path, label: "Notes", id: "notes")
        _ = try await fixture.clientB.enable(preset: "custom", path: dirB.path, label: "Notes", id: "notes")
        try "before".write(to: dirA.appendingPathComponent("a.md"), atomically: true, encoding: .utf8)
        try await fixture.clientA.sync()
        try await waitUntil("the first file to arrive at B") {
            (try? String(contentsOf: dirB.appendingPathComponent("a.md"), encoding: .utf8)) == "before"
        }

        let paused = try await fixture.clientB.pause(libraryID: "notes")
        XCTAssertTrue(paused.paused, "the library didn't report paused")

        try "while paused".write(to: dirA.appendingPathComponent("a.md"), atomically: true, encoding: .utf8)
        // A tries to sync; B refuses while it's paused, and that refusal is
        // the point — the change waits in A.
        do { try await fixture.clientA.sync() } catch { }
        try await waitUntil("A to show the new content in its index") {
            (try? String(contentsOf: dirA.appendingPathComponent("a.md"), encoding: .utf8)) == "while paused"
        }
        let atB = try String(contentsOf: dirB.appendingPathComponent("a.md"), encoding: .utf8)
        XCTAssertEqual(atB, "before", "a paused library received a change")

        _ = try await fixture.clientB.resume(libraryID: "notes")
        try await fixture.clientB.sync()
        try await waitUntil("the change to arrive after resuming") {
            (try? String(contentsOf: dirB.appendingPathComponent("a.md"), encoding: .utf8)) == "while paused"
        }
    }

    /// The event stream carries what happens, like a pause.
    func testEventsStreamArrive() async throws {
        let fixture = try await DaemonFixture(two: false)
        defer { fixture.stop() }

        let dirA = fixture.temporaryDirectory("notes-A")
        _ = try await fixture.clientA.enable(preset: "custom", path: dirA.path, label: "Notes", id: "notes")

        var got: [SynkEvent] = []
        let stream = fixture.clientA.events()
        let collecting = Task {
            for try await e in stream {
                got.append(e)
                if e.kind == "library" { break }
            }
        }
        _ = try await fixture.clientA.pause(libraryID: "notes")
        for _ in 0..<100 {
            if got.contains(where: { $0.kind == "library" }) { break }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        collecting.cancel()
        let pauseEvent = got.first { $0.kind == "library" }
        XCTAssertNotNil(pauseEvent, "no library event arrived; got \(got.map(\.message))")
        XCTAssertEqual(pauseEvent?.library, "notes", "the event doesn't name the library")
    }

    /// A synk://pair link parses, as the Go side writes it.
    func testPairLinkParsing() {
        let raw = "synk://pair?v=2&code=ABCD-EFGH-JKLM-NPQR&token=abcdef0123456789&fp=01%3AAB" +
            "&addr=192.168.1.10%3A7420&addr=%5Bfd00%3A%3A1%5D%3A7420"
        let link = PairLink(link: raw)
        XCTAssertEqual(link?.code, "ABCD-EFGH-JKLM-NPQR")
        XCTAssertEqual(link?.token, "abcdef0123456789")
        XCTAssertEqual(link?.fingerprint, "01:AB")
        XCTAssertEqual(link?.addrs, ["192.168.1.10:7420", "[fd00::1]:7420"])
        XCTAssertNil(PairLink(link: "https://example.com/pair?code=A&token=B&fp=C"))
        XCTAssertNil(PairLink(link: "synk://pair?code=A&token=B"))
    }

    private func waitUntil(_ what: String, tries: Int = 100, _ condition: () throws -> Bool) async throws {
        for _ in 0..<tries {
            if try condition() { return }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        XCTFail("waited 10 s for \(what)")
    }
}

/// Runs real daemons on temporary data folders with their own ports.
final class DaemonFixture {
    let dirA: URL
    let dirB: URL
    let clientA: SynkClient
    let clientB: SynkClient
    private var processes: [Process] = []
    private static var roots: [URL] = []

    /// Where the daemon lives once `make build` ran.
    static var daemonPath: String {
        Self.repoRoot.appendingPathComponent("bin/synk").path
    }

    static var repoRoot: URL {
        // apps/tests/SynkClientTests.swift → two levels up is the repo.
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
    }

    init(two: Bool) async throws {
        let root = FileManager.default.temporaryDirectory
            .appendingPathComponent("synk-swift-tests-\(UUID().uuidString)")
        Self.roots.append(root)
        dirA = root.appendingPathComponent("a")
        dirB = root.appendingPathComponent("b")
        for dir in [dirA, dirB] {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        }
        clientA = try await Self.start(dir: dirA, root: root)
        if two {
            clientB = try await Self.start(dir: dirB, root: root)
        } else {
            clientB = clientA
        }
    }

    func temporaryDirectory(_ name: String) -> URL {
        let url = dirA.deletingLastPathComponent().appendingPathComponent(name)
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    func stop() {
        for p in processes where p.isRunning {
            p.terminate()
        }
        processes.removeAll()
        // Only when the folder is still there: a second stop (from a
        // caller that also stopped us) is a no-op.
        if FileManager.default.fileExists(atPath: dirA.deletingLastPathComponent().path) {
            try? FileManager.default.removeItem(at: dirA.deletingLastPathComponent())
        }
    }

    /// Spawns a daemon on a fresh data folder and returns a client for it
    /// once the daemon answers /v1/health.
    private static func start(dir: URL, root: URL) async throws -> SynkClient {
        let port = try freePort()
        let config = """
        {"version":1,"listen":"127.0.0.1:0","api":"127.0.0.1:\(port)"}
        """
        try config.write(to: dir.appendingPathComponent("config.json"), atomically: true, encoding: .utf8)
        let p = Process()
        p.executableURL = URL(fileURLWithPath: daemonPath)
        p.arguments = ["serve"]
        var env = ProcessInfo.processInfo.environment
        env["SYNK_DIR"] = dir.path
        p.environment = env
        let log = root.appendingPathComponent("daemon-\(dir.lastPathComponent).log")
        FileManager.default.createFile(atPath: log.path, contents: nil)
        p.standardOutput = try FileHandle(forWritingTo: log)
        p.standardError = p.standardOutput
        try p.run()

        // The daemon writes its API token as the first thing it does; wait
        // for it, or report the daemon's own output if it died.
        let tokenURL = dir.appendingPathComponent("api-token")
        for _ in 0..<100 {
            if (try? String(contentsOf: tokenURL, encoding: .utf8)) != nil {
                break
            }
            if !p.isRunning {
                let out = (try? String(contentsOf: log, encoding: .utf8)) ?? ""
                throw SynkAPIError(status: 0, message: "the daemon exited at once: \(out.suffix(400))")
            }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        guard FileManager.default.fileExists(atPath: tokenURL.path) else {
            throw SynkAPIError(status: 0, message: "the daemon didn't write its API token in 10 s")
        }
        let client = try SynkClient(base: SynkClient.base(apiAddress: "127.0.0.1:\(port)"), dataDirectory: dir)
        try await waitHealthy(client)
        return client
    }

    private static func waitHealthy(_ client: SynkClient) async throws {
        var lastError: Error?
        for _ in 0..<100 {
            do {
                if try await client.health().ok {
                    return
                }
            } catch {
                lastError = error
            }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        XCTFail("the daemon didn't answer /v1/health in 10 s: \(String(describing: lastError))")
    }

    private static func freePort() throws -> Int {
        let sock = socket(AF_INET, SOCK_STREAM, 0)
        guard sock >= 0 else {
            throw SynkAPIError(status: 0, message: "couldn't open a socket to find a free port")
        }
        defer { close(sock) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_addr.s_addr = INADDR_LOOPBACK.bigEndian
        let size = socklen_t(MemoryLayout<sockaddr_in>.size)
        let bound = addr.withSockaddr { bind(sock, $0, size) }
        guard bound == 0 else {
            throw SynkAPIError(status: 0, message: "couldn't bind to port 0 to find a free port")
        }
        var name = sockaddr_in()
        var len: socklen_t = size
        let read = withUnsafeMutablePointer(to: &name) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                getsockname(sock, sa, &len)
            }
        }
        guard read == 0 else {
            throw SynkAPIError(status: 0, message: "couldn't read the chosen port")
        }
        return Int(CFSwapInt16BigToHost(name.sin_port))
    }
}

private extension sockaddr_in {
    func withSockaddr(_ body: (UnsafePointer<sockaddr>) -> Int32) -> Int32 {
        withUnsafePointer(to: self) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { body($0) }
        }
    }
}
