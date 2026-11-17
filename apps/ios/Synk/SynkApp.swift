import SwiftUI
import SynkCore
import UIKit

// The iOS app embeds the engine through the mobile package's generated
// framework: there is no separate daemon on the phone, so the model owns
// the node lifecycle (decision 0008). Status and pairing answers cross
// the bridge as JSON shaped like the REST API's, decoded with the shared
// Codable types.
//
// Libraries live in the app's Documents folder, one per app preset, so
// Files shows them under On My iPhone → Synk and the apps that read
// those formats can open the files in place (decision 0010). The phone
// cannot pick folders outside its sandbox; the File Provider extension
// is the later, broader answer.

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
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationStack {
            List {
                Section("This device") {
                    if let status = model.status {
                        Label(status.name, systemImage: "iphone.gen3")
                        Text("ID \(status.deviceId)").font(.caption).foregroundStyle(.secondary)
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
                LibrariesSection()
                AddLibrarySection()
                PairSection()
                if let error = model.error {
                    Section("Problems") {
                        Label(error, systemImage: "exclamationmark.octagon")
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Synk")
            .onChange(of: scenePhase) { phase in
                if phase == .active {
                    Task { await model.refresh() }
                }
            }
        }
    }
}

struct LibrariesSection: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        Section("Libraries") {
            if let libraries = model.status?.libraries, !libraries.isEmpty {
                ForEach(libraries) { library in
                    VStack(alignment: .leading, spacing: 2) {
                        HStack {
                            Label(library.label, systemImage: "folder")
                            Spacer()
                            if library.paused {
                                Text("Paused").font(.caption2).foregroundStyle(.secondary)
                            }
                        }
                        Text((library.path as NSString).lastPathComponent)
                            .font(.caption2).foregroundStyle(.secondary)
                        if let error = library.error {
                            Text(error).font(.caption2).foregroundStyle(.red)
                        }
                    }
                    .swipeActions(edge: .trailing) {
                        Button("Remove") {
                            Task { await model.removeLibrary(library) }
                        }.tint(.red)
                        Button(library.paused ? "Resume" : "Pause") {
                            Task {
                                if library.paused {
                                    await model.resumeLibrary(library)
                                } else {
                                    await model.pauseLibrary(library)
                                }
                            }
                        }.tint(.orange)
                    }
                }
                Button("Sync now") {
                    Task { await model.syncNow() }
                }
            } else {
                Text("Nothing synced yet. Add a library below.")
                    .foregroundStyle(.secondary)
            }
        }
    }
}

/// The catalog presets that work on this device. Adding one creates its
/// folder in the app's Documents and starts syncing it — on iOS the user
/// picks the app, not a folder; the sandbox decides where files live.
struct AddLibrarySection: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        Section("Add a library") {
            if model.catalog.isEmpty {
                Text(model.status == nil ? "Loading…" : "Nothing to add.")
                    .foregroundStyle(.secondary)
            }
            ForEach(model.catalog) { preset in
                VStack(alignment: .leading, spacing: 2) {
                    HStack {
                        Text(preset.name)
                        Spacer()
                        Button("Add") {
                            Task { await model.addLibrary(preset) }
                        }
                        .buttonStyle(.borderless)
                    }
                    if let hint = preset.iosHint {
                        Text(hint).font(.caption2).foregroundStyle(.secondary)
                    }
                }
            }
            if let message = model.libraryMessage {
                Text(message).font(.caption).foregroundStyle(.secondary)
            }
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
    @Published private(set) var catalog: [Preset] = []
    @Published private(set) var error: String?
    @Published private(set) var pairMessage: String?
    @Published private(set) var libraryMessage: String?

    private var engine: MobileSynk?
    private let dir: URL
    private let documents: URL

    init() {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        dir = support.appendingPathComponent("Synk", isDirectory: true)
        documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
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
            await loadCatalog()
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

    /// Loads the catalog and keeps the presets that work on iOS.
    func loadCatalog() async {
        guard let engine else { return }
        do {
            let all: [Preset] = try decodeBridge(engine.catalog())
            catalog = all.filter { preset in
                preset.platforms?.contains("ios") ?? false
            }
        } catch {
            self.error = String(describing: error)
        }
    }

    /// Adds a preset's library in the app's Documents folder. The folder
    /// name is the preset's name, so Files shows On My iPhone → Synk →
    /// Obsidian, and the same app preset syncs with the Mac by ID.
    func addLibrary(_ preset: Preset) async {
        guard let engine else { return }
        do {
            let folder = documents.appendingPathComponent(preset.name, isDirectory: true)
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            let library: SynkLibrary = try decodeBridge(engine.addLibrary(preset.id, label: "", path: folder.path))
            _ = library
            libraryMessage = "Added \(preset.name)."
            await refresh()
        } catch {
            libraryMessage = "Adding failed: \(error.localizedDescription)"
        }
    }

    func pauseLibrary(_ library: LibraryState) async {
        guard let engine else { return }
        do {
            try engine.pauseLibrary(library.id)
            await refresh()
        } catch {
            libraryMessage = "Pausing failed: \(error.localizedDescription)"
        }
    }

    func resumeLibrary(_ library: LibraryState) async {
        guard let engine else { return }
        do {
            try engine.resumeLibrary(library.id)
            await refresh()
        } catch {
            libraryMessage = "Resuming failed: \(error.localizedDescription)"
        }
    }

    /// Forgets the library. The engine keeps the files, versions, and
    /// index (decision 0005), so adding the preset again resumes.
    func removeLibrary(_ library: LibraryState) async {
        guard let engine else { return }
        do {
            try engine.removeLibrary(library.id)
            await refresh()
        } catch {
            libraryMessage = "Removing failed: \(error.localizedDescription)"
        }
    }

    func syncNow() async {
        guard let engine else { return }
        do {
            try engine.syncNow()
            libraryMessage = "Synced."
            await refresh()
        } catch {
            libraryMessage = "Sync failed: \(error.localizedDescription)"
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
