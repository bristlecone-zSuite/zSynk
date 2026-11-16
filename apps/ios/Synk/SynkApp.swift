import SwiftUI
import SynkCore
import UIKit

// The iOS app embeds the engine through the mobile package's generated
// framework: there is no separate daemon on the phone, so the model owns
// the node lifecycle (decision 0008). Status and pairing answers cross
// the bridge as JSON shaped like the REST API's, decoded with the shared
// Codable types.

@main
struct SynkApp: App {
    @StateObject private var model = SynkModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(model)
                .task { await model.start() }
        }
    }
}

struct RootView: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        NavigationStack {
            List {
                Section("This device") {
                    if let status = model.status {
                        Label(status.name, systemImage: "iphone.gen3")
                        Text("ID \(status.deviceId)").font(.caption).foregroundStyle(.secondary)
                    } else if let error = model.error {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    } else {
                        ProgressView().controlSize(.small)
                    }
                }
                Section("Paired devices") {
                    if let peers = model.status?.peers, !peers.isEmpty {
                        ForEach(peers) { peer in
                            Label(peer.name, systemImage: "laptopcomputer.and.iphone")
                        }
                    } else {
                        Text("No devices yet. Pair with a Mac below.")
                            .foregroundStyle(.secondary)
                    }
                }
                PairSection()
            }
            .navigationTitle("Synk")
        }
    }
}

struct PairSection: View {
    @EnvironmentObject var model: SynkModel
    @State private var link = ""

    var body: some View {
        Section("Pair with a Mac") {
            TextField("synk://pair?…", text: $link)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            Button("Paste link") {
                if let text = UIPasteboard.general.string { link = text }
            }
            Button("Pair") {
                let candidate = link
                link = ""
                Task { await model.pairWithLink(candidate) }
            }
            .disabled(link.isEmpty)
            if let message = model.pairMessage {
                Text(message).font(.caption).foregroundStyle(.secondary)
            }
        }
    }
}

@MainActor
final class SynkModel: ObservableObject {
    @Published private(set) var status: SynkStatus?
    @Published private(set) var error: String?
    @Published private(set) var pairMessage: String?

    private var engine: MobileSynk?
    private let dir: URL

    init() {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        dir = support.appendingPathComponent("Synk", isDirectory: true)
    }

    /// Opens the embedded node and keeps the status fresh. The engine
    /// runs on background threads; every call hops through a task.
    func start() async {
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            var openError: NSError?
            engine = MobileOpen(dir.path, UIDevice.current.name, &openError)
            if let openError {
                error = openError.localizedDescription
                return
            }
            await refresh()
        } catch {
            self.error = String(describing: error)
        }
    }

    func refresh() async {
        guard let engine else { return }
        do {
            status = try decodeBridge(engine.status())
        } catch {
            self.error = String(describing: error)
        }
    }

    func pairWithLink(_ link: String) async {
        guard let engine else { return }
        do {
            try engine.pair(withMac: link)
            pairMessage = "Paired."
            await refresh()
        } catch {
            pairMessage = "Pairing failed: \(error)"
        }
    }
}

// The string-returning engine methods answer with {"ok":…} envelopes
// instead of a thrown error (decision 0009); this unwraps them.
private func decodeBridge<T: Decodable>(_ envelope: String) throws -> T {
    let wrapper = try JSONDecoder.synk.decode(BridgeEnvelope<T>.self, from: Data(envelope.utf8))
    if wrapper.ok, let data = wrapper.data {
        return data
    }
    throw BridgeError(wrapper.error ?? "the engine returned no answer")
}

private struct BridgeEnvelope<T: Decodable>: Decodable {
    let ok: Bool
    let data: T?
    let error: String?
}

private struct BridgeError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}
