import Foundation

// MARK: - Types mirroring the daemon's JSON

struct SynkHealth: Codable {
    let ok: Bool
    let name: String
}

/// The device, its libraries, and its peers, as `GET /v1/status` answers.
struct SynkStatus: Codable {
    let deviceId: String
    let name: String
    let listen: String?
    let api: String?
    let libraries: [LibraryState]
    let peers: [SynkPeer]
}

/// A library as the config holds it: what enable, pause, and resume
/// answer with. The fuller view with counts and errors is `LibraryState`.
struct SynkLibrary: Codable, Identifiable {
    let id: String
    let preset: String
    let label: String
    let path: String
    let paused: Bool
    let shareWith: [String]?
}

/// One library on this device. `paused` is true while `synk pause` holds it.
struct LibraryState: Codable, Identifiable {
    var id: String { libraryID }
    let libraryID: String
    let preset: String
    let label: String
    let path: String
    let paused: Bool
    let files: Int
    let devices: [DeviceSync]
    let heldDeletes: [String]?
    let placeholders: [String]?
    let scanErrors: [String]?
    let skipped: [String]?
    let busy: [String]?
    let error: String?

    enum CodingKeys: String, CodingKey {
        case libraryID = "id"
        case preset, label, path, paused, files, devices
        case heldDeletes, placeholders, scanErrors, skipped, busy, error
    }
}

/// How a library has synced with one device since the daemon started.
struct DeviceSync: Codable, Identifiable {
    var id: String { deviceId }
    let deviceId: String
    let name: String
    let lastSync: Date?
    let failures: Int?
    let error: String?
    let errorAt: Date?
    let retryAt: Date?
}

/// A paired device.
struct SynkPeer: Codable, Identifiable {
    var id: String { deviceId }
    let deviceId: String
    let name: String
    let publicKey: String
    let addresses: [String]?
    let pairedAt: Date
    let lastSeen: Date?
}

/// A preset from the app catalog.
struct Preset: Codable, Identifiable {
    let id: String
    let name: String
    let summary: String
    let kind: String
    let hint: String
    let warning: String?
    let iosHint: String?
}

struct CatalogResponse: Codable {
    let catalog: [Preset]
    let discovered: [DiscoveredApp]
}

/// An app the daemon found on this Mac, with where its files live.
struct DiscoveredApp: Codable, Identifiable {
    var id: String { preset.id }
    let preset: Preset
    let paths: [String]
    /// Paths under iCloud Drive or another cloud provider. Synk won't sync
    /// those in place; the UI offers to move the library to a local folder.
    let cloudPaths: [String]
}

/// A version the vault keeps of a file.
struct SynkVersion: Codable, Identifiable {
    let id: String
    let path: String
    /// "file" or "package".
    let kind: String
    let hash: String
    let at: Date
    /// "pre-overwrite" for replaced content, "pre-delete" for deleted content.
    let reason: String
    let size: Int64
}

/// What another device needs to pair with this one.
struct PairOffer: Codable {
    let code: String
    let token: String
    let deviceId: String
    let name: String
    let fingerprint: String
    let addresses: [String]
    /// A synk://pair link that carries all of the above.
    let qr: String
    let listen: String?
    let expiresAt: Date
}

/// Something that happened on the device, from `GET /v1/events`.
struct SynkEvent: Codable, Identifiable {
    let at: Date
    /// scan, sync, deletes, pairing, library, or peer.
    let kind: String
    let library: String?
    let device: String?
    /// info, or warning for what needs a person.
    let level: String?
    let message: String

    var id: String { "\(at.timeIntervalSince1970)-\(kind)-\(message)" }
}

/// What `deletes/restore` answers: the files it put back, and why some
/// couldn't be put back yet, if any.
struct RestoredDeletes: Codable {
    let paths: [String]
    let warning: String?
}

/// An error the daemon answered with: its message, the HTTP status, and
/// the API path that failed.
struct SynkAPIError: Error, Decodable {
    let status: Int
    let message: String
    var path: String = ""

    var localizedDescription: String { path.isEmpty ? message : "\(path): \(message)" }
}

// MARK: - The client

/// Talks to the daemon's local API: loopback only, with the bearer token
/// from the data folder. The same client serves the Mac app and, later,
/// the iOS app talking to a paired Mac.
struct SynkClient {
    var base: URL
    var token: String
    var session: URLSession = .shared

    /// The file in the data folder that holds the API token.
    static func tokenPath(dataDirectory: URL) -> URL {
        dataDirectory.appendingPathComponent("api-token")
    }

    /// Makes a client for a daemon whose data folder is at `dataDirectory`,
    /// reading its token from there.
    init(base: URL, dataDirectory: URL) throws {
        self.base = base
        let tokenURL = SynkClient.tokenPath(dataDirectory: dataDirectory)
        guard let raw = try? String(contentsOf: tokenURL, encoding: .utf8) else {
            throw SynkAPIError(status: 0, message: "no API token at \(tokenURL.path); is Synk running?")
        }
        token = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if token.isEmpty {
            throw SynkAPIError(status: 0, message: "the API token file at \(tokenURL.path) is empty")
        }
    }

    init(base: URL, token: String) {
        self.base = base
        self.token = token
    }

    func health() async throws -> SynkHealth {
        try await get("health")
    }

    func status() async throws -> SynkStatus {
        try await get("status")
    }

    func catalog() async throws -> CatalogResponse {
        try await get("apps")
    }

    func libraries() async throws -> [LibraryState] {
        try await get("libraries")
    }

    /// Adds a library: a preset, and a path when the preset wants one
    /// picked. Answers with the library as it is now configured.
    func enable(preset: String, path: String?, label: String?, id: String?) async throws -> SynkLibrary {
        var body: [String: String] = ["preset": preset]
        if let path { body["path"] = path }
        if let label { body["label"] = label }
        if let id { body["id"] = id }
        return try await post("libraries", body: body)
    }

    func pause(libraryID: String) async throws -> SynkLibrary {
        struct Paused: Decodable { let library: SynkLibrary }
        let out: Paused = try await post("libraries/\(libraryID)/pause", body: nil)
        return out.library
    }

    func resume(libraryID: String) async throws -> SynkLibrary {
        struct Resumed: Decodable { let library: SynkLibrary }
        let out: Resumed = try await post("libraries/\(libraryID)/resume", body: nil)
        return out.library
    }

    /// Forgets a library. The daemon keeps the files, the versions, and the
    /// index, so enabling the folder again picks up where it left off.
    func remove(libraryID: String) async throws {
        try await delete("libraries/\(libraryID)")
    }

    /// Forgets a paired device: every library stops sharing with it, and
    /// its connections are refused.
    func unpair(deviceID: String) async throws {
        try await delete("peers/\(deviceID)")
    }

    /// Sends the held deletes the user confirmed. They must still be the
    /// ones held.
    func confirmDeletes(libraryID: String, paths: [String]) async throws -> [String] {
        let out: PathsResponse = try await post("libraries/\(libraryID)/deletes/confirm", body: ["paths": paths])
        return out.paths
    }

    /// Puts every held delete back.
    func restoreDeletes(libraryID: String) async throws -> RestoredDeletes {
        try await post("libraries/\(libraryID)/deletes/restore", body: nil)
    }

    /// Lists the versions the vault keeps of a library's files, newest
    /// first: every file's, or with `path`, one file's.
    func versions(libraryID: String, path: String? = nil) async throws -> [SynkVersion] {
        let versionsURL = base.appending(path: "v1/libraries/\(libraryID)/versions")
        var parts = URLComponents(url: versionsURL, resolvingAgainstBaseURL: false)!
        if let path {
            parts.queryItems = [URLQueryItem(name: "path", value: path)]
        }
        var req = URLRequest(url: parts.url!)
        req.httpMethod = "GET"
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let out: VersionsResponse = try await run(req)
        return out.versions
    }

    /// Puts a version of a file back; without `version`, the newest. What
    /// it replaces is kept as a version in turn.
    @discardableResult
    func restore(libraryID: String, path: String, version: String?) async throws -> SynkVersion {
        var body: [String: String] = ["path": path]
        if let version { body["version"] = version }
        let out: RestoreResponse = try await post("libraries/\(libraryID)/versions/restore", body: body)
        return out.restored
    }

    /// Arms this device to accept one joining device, and answers with the
    /// offer to show as a code or a QR.
    func startPair() async throws -> PairOffer {
        try await post("pair/start", body: nil)
    }

    /// The offer waiting for a device to join, if it hasn't expired.
    func pairCode() async throws -> PairOffer {
        try await get("pair/code")
    }

    /// Joins a device that made an offer, trying each of the offer's
    /// addresses. The fingerprint pins the answering device's key, so no
    /// other device can answer the offer.
    func joinPair(_ link: PairLink) async throws {
        struct JoinOK: Decodable { let ok: Bool }
        var lastError: Error?
        for addr in link.addrs {
            do {
                let _: JoinOK = try await post(
                    "pair/join",
                    body: ["addr": addr, "code": link.code, "token": link.token, "fingerprint": link.fingerprint]
                )
                return
            } catch {
                lastError = error
            }
        }
        throw lastError ?? SynkAPIError(status: 0, message: "the offer lists no address to join")
    }

    /// Syncs one library with one peer, or with nothing given, everything.
    func sync(addr: String? = nil, folderID: String? = nil) async throws {
        struct SyncOK: Decodable { let ok: Bool }
        var body: [String: String] = [:]
        if let addr { body["addr"] = addr }
        if let folderID { body["folderId"] = folderID }
        let _: SyncOK = try await post("sync", body: body)
    }

    /// Streams what happens on the device as it happens. The stream ends
    /// when the daemon hangs up; a reader that falls behind loses events
    /// rather than slowing the daemon down.
    func events() -> AsyncThrowingStream<SynkEvent, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                var req = URLRequest(url: base.appending(path: "v1/events"))
                req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
                let (bytes, response) = try await session.bytes(for: req)
                guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                    let code = (response as? HTTPURLResponse)?.statusCode ?? 0
                    throw SynkAPIError(status: code, message: "the event stream didn't open")
                }
                var line = ""
                for try await byte in bytes {
                    if byte == 0x0A {
                        if line.hasPrefix("data: ") {
                            if let e = try? JSONDecoder.synk.decode(SynkEvent.self,
                                                               from: Data(line.dropFirst(6).utf8)) {
                                continuation.yield(e)
                            }
                        }
                        line = ""
                    } else if byte != 0x0D {
                        line.append(Character(UnicodeScalar(byte)))
                    }
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: Plumbing

    private struct PathsResponse: Decodable { let paths: [String] }
    private struct VersionsResponse: Decodable { let versions: [SynkVersion] }
    private struct RestoreResponse: Decodable { let restored: SynkVersion }

    private func request(_ method: String, _ path: String, body: Data?) -> URLRequest {
        var req = URLRequest(url: base.appending(path: "v1/" + path))
        req.httpMethod = method
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = body
        }
        return req
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        try await run(request("GET", path, body: nil))
    }

    private func post<T: Decodable>(_ path: String, body: [String: Any]?) async throws -> T {
        let data = try body.map { try JSONSerialization.data(withJSONObject: $0) } ?? Data("{}".utf8)
        return try await run(request("POST", path, body: data))
    }

    private func delete(_ path: String) async throws {
        struct Acknowledged: Decodable { let ok: Bool }
        let _: Acknowledged = try await run(request("DELETE", path, body: Data("{}".utf8)))
    }

    private func run<T: Decodable>(_ req: URLRequest) async throws -> T {
        let path = req.url?.path ?? ""
        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw SynkAPIError(status: 0, message: "the daemon didn't answer", path: path)
        }
        guard (200..<300).contains(http.statusCode) else {
            var message = "the daemon answered \(http.statusCode)"
            if let e = try? JSONDecoder().decode(SynkAPIErrorBody.self, from: data) {
                message = e.error
            }
            throw SynkAPIError(status: http.statusCode, message: message, path: path)
        }
        return try JSONDecoder.synk.decode(T.self, from: data)
    }
}

private struct SynkAPIErrorBody: Decodable {
    let error: String
}

extension JSONDecoder {
    /// Decodes the daemon's JSON: dates as RFC 3339.
    static var synk: JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }
}

/// A synk://pair link, as a QR code or the pairing screen carries it. The
/// Go side writes it (pairing.Link.URI); parsing it here keeps the format
/// in one place on the joining side.
struct PairLink {
    /// The offering device's addresses, tried in order.
    let addrs: [String]
    let code: String
    let token: String
    let fingerprint: String

    init?(link: String) {
        let s = link.trimmingCharacters(in: .whitespacesAndNewlines)
        guard s.hasPrefix("synk://pair?") else {
            return nil
        }
        guard let url = URL(string: s), let query = url.query else {
            return nil
        }
        var addrs: [String] = []
        var dict: [String: String] = [:]
        for pair in query.split(separator: "&") {
            let kv = pair.split(separator: "=", maxSplits: 1)
            guard kv.count == 2, let k = kv[0].removingPercentEncoding, let v = kv[1].removingPercentEncoding else {
                continue
            }
            if k == "addr" {
                addrs.append(v)
            } else {
                dict[k] = v
            }
        }
        guard let code = dict["code"], let token = dict["token"], let fp = dict["fp"] else {
            return nil
        }
        self.addrs = addrs
        self.code = code
        self.token = token
        fingerprint = fp
    }
}
