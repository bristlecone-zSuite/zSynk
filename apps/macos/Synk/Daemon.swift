import Foundation

/// Keeps the bundled `synk` daemon running and answers where it is. The
/// app never writes the data folder itself: the daemon is the only writer
/// (the single-writer rule), and the app talks to it through the local
/// API with the token the daemon made.
@MainActor
final class Daemon: ObservableObject {
    /// Where the daemon keeps everything: the data folder under
    /// Application Support.
    let dataDirectory: URL
    /// How the app reaches the API. The daemon's config picks the port;
    /// the app follows what the config says once it exists.
    @Published private(set) var apiAddress: String

    @Published private(set) var up = false
    @Published private(set) var lastError: String?

    private var process: Process?

    nonisolated static var defaultDataDirectory: URL {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return support.appendingPathComponent("Synk", isDirectory: true)
    }

    init(dataDirectory: URL = Daemon.defaultDataDirectory) {
        self.dataDirectory = dataDirectory
        apiAddress = "127.0.0.1:7421"
    }

    /// The daemon inside this app's bundle, next to the app's own binary.
    /// It's named synk-daemon because "synk" would overwrite the app
    /// binary itself on a case-insensitive volume.
    var bundledExecutable: URL? {
        Bundle.main.executableURL?.deletingLastPathComponent().appendingPathComponent("synk-daemon")
    }

    /// Makes sure a daemon is serving this data folder, and returns a
    /// client for it. A daemon someone started by hand (synk serve) is
    /// reused rather than replaced.
    func startIfNeeded() async throws -> SynkClient {
        try FileManager.default.createDirectory(at: dataDirectory, withIntermediateDirectories: true)
        readAPIAddress()
        let client = try SynkClient(base: SynkClient.base(apiAddress: apiAddress), dataDirectory: dataDirectory)
        if let health = try? await client.health(), health.ok {
            up = true
            lastError = nil
            return client
        }
        guard let exe = bundledExecutable, FileManager.default.isExecutableFile(atPath: exe.path) else {
            throw SynkAPIError(status: 0, message: "the Synk app doesn't carry the synk daemon; rebuild the app")
        }
        try launchDaemon(at: exe)
        // The daemon needs a moment: the first run makes an identity.
        for attempt in 0..<100 {
            if let health = try? await client.health(), health.ok {
                up = true
                lastError = nil
                return client
            }
            if process?.isRunning == false {
                break
            }
            try await Task.sleep(nanoseconds: 100_000_000 + UInt64(attempt) * 5_000_000)
        }
        up = false
        throw SynkAPIError(status: 0, message: "the Synk daemon didn't start")
    }

    private func launchDaemon(at exe: URL) throws {
        if process?.isRunning == true {
            return
        }
        let p = Process()
        p.executableURL = exe
        p.arguments = ["serve"]
        var env = ProcessInfo.processInfo.environment
        env["SYNK_DIR"] = dataDirectory.path
        p.environment = env
        let log = dataDirectory.appendingPathComponent("app-daemon.log")
        FileManager.default.createFile(atPath: log.path, contents: nil)
        p.standardOutput = try FileHandle(forWritingTo: log)
        p.standardError = p.standardOutput
        p.terminationHandler = { [weak self] _ in
            Task { @MainActor in
                self?.processDidExit()
            }
        }
        try p.run()
        process = p
    }

    private func processDidExit() {
        process = nil
        up = false
    }

    /// Reads the API address from the daemon's config, so an unusual port
    /// still works.
    private func readAPIAddress() {
        let url = dataDirectory.appendingPathComponent("config.json")
        guard let data = try? Data(contentsOf: url),
              let cfg = try? JSONDecoder().decode(DaemonConfig.self, from: data),
              !cfg.api.isEmpty else {
            return
        }
        apiAddress = cfg.api
    }

    func stop() {
        process?.terminate()
    }
}

private struct DaemonConfig: Decodable {
    let api: String
}

extension SynkClient {
    static func base(apiAddress: String) -> URL {
        URL(string: "http://\(apiAddress)")!
    }
}
