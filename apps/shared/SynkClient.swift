import Foundation

struct SynkHealth: Codable { let ok: Bool; let name: String }

struct SynkStatus: Codable {
    let deviceId: String
    let name: String
    let libraries: [LibraryState]
    let peers: [Peer]
}

struct LibraryState: Codable, Identifiable {
    var id: String { self.libraryID }
    let libraryID: String
    let preset: String
    let label: String
    let path: String
    let paused: Bool
    let files: Int

    enum CodingKeys: String, CodingKey {
        case libraryID = "id"
        case preset, label, path, paused, files
    }
}

struct Peer: Codable, Identifiable {
    var id: String { deviceId }
    let deviceId: String
    let name: String
    let publicKey: String
}

struct PairOffer: Codable {
    let code: String
    let token: String
    let deviceId: String
    let name: String
    let qr: String?
    let listen: String?
}

struct CatalogResponse: Codable {
    let catalog: [Preset]
}

struct Preset: Codable, Identifiable {
    let id: String
    let name: String
    let summary: String
    let kind: String
    let hint: String
    let warning: String?
    let iosHint: String?
}

final class SynkClient {
    var base = URL(string: "http://127.0.0.1:7421")!

    func health() async throws -> SynkHealth {
        try await get("/v1/health")
    }

    func status() async throws -> SynkStatus {
        try await get("/v1/status")
    }

    func apps() async throws -> CatalogResponse {
        try await get("/v1/apps")
    }

    func enable(preset: String, path: String?) async throws {
        var req = URLRequest(url: base.appending(path: "/v1/libraries"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        var body: [String: String] = ["preset": preset]
        if let path { body["path"] = path }
        req.httpBody = try JSONEncoder().encode(body)
        _ = try await URLSession.shared.data(for: req)
    }

    func startPair() async throws -> PairOffer {
        var req = URLRequest(url: base.appending(path: "/v1/pair/start"))
        req.httpMethod = "POST"
        let (data, _) = try await URLSession.shared.data(for: req)
        return try JSONDecoder().decode(PairOffer.self, from: data)
    }

    func joinPair(addr: String, code: String, token: String) async throws {
        var req = URLRequest(url: base.appending(path: "/v1/pair/join"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["addr": addr, "code": code, "token": token])
        _ = try await URLSession.shared.data(for: req)
    }

    func sync(addr: String?, folderId: String?) async throws {
        var req = URLRequest(url: base.appending(path: "/v1/sync"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        var body: [String: String] = [:]
        if let addr { body["addr"] = addr }
        if let folderId { body["folderId"] = folderId }
        req.httpBody = try JSONEncoder().encode(body)
        _ = try await URLSession.shared.data(for: req)
    }

    private func get<T: Decodable>(_ path: String) async throws -> T {
        let (data, _) = try await URLSession.shared.data(from: base.appending(path: path))
        return try JSONDecoder().decode(T.self, from: data)
    }
}
