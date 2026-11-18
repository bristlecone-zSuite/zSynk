import SwiftUI
import SynkCore
import UIKit
import Vision
import VisionKit
import FileProvider

// The iOS app embeds the engine through the mobile package's generated
// framework: there is no separate daemon on the phone, so the model owns
// the node lifecycle (decision 0008). Status and pairing answers cross
// the bridge as JSON shaped like the REST API's, decoded with the shared
// Codable types.
//
// Libraries live in the app group container's Documents folder, one per
// app preset, and the File Provider extension shows them in Files under
// On My iPhone → Synk (decision 0011). When the app group is
// unavailable (the entitlement is missing on the device), the app falls
// back to its own Documents folder and the old Files exposure keeps
// working. The phone cannot pick folders outside its sandbox.

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
                if let error = model.fileProviderError {
                    Section("Files") {
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
/// folder in the app group container's Documents and starts syncing it —
/// on iOS the user picks the app, not a folder; the sandbox decides
/// where files live.
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
    @State private var scanning = false

    var body: some View {
        Section("Pair with a Mac") {
            TextField("synk://pair?…", text: $link)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
            Button("Scan QR") { scanning = true }
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
        .sheet(isPresented: $scanning) {
            QRScannerView { scanned in
                scanning = false
                Task { await model.pairWithLink(scanned) }
            }
        }
    }
}

/// Wraps DataScannerViewController (iOS 16+) — the system's QR scanner,
/// so there is no camera plumbing to get wrong. The scanned string is
/// the same synk://pair link the user can paste.
struct QRScannerView: UIViewControllerRepresentable {
    let onScan: (String) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onScan: onScan) }

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let controller = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])])
        controller.delegate = context.coordinator
        return controller
    }

    func updateUIViewController(_ uiViewController: DataScannerViewController, context: Context) {}

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        private let onScan: (String) -> Void

        init(onScan: @escaping (String) -> Void) {
            self.onScan = onScan
        }

        func dataScanner(_ controller: DataScannerViewController,
                         didAdd addedItems: [RecognizedItem], allItems: [RecognizedItem]) {
            let strings = addedItems.compactMap { item -> String? in
                if case .barcode(let barcode) = item { return barcode.payloadStringValue }
                return nil
            }
            guard let link = strings.first(where: { PairLink(link: $0) != nil }) else { return }
            controller.stopScanning()
            onScan(link)
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
    @Published private(set) var fileProviderError: String?

    private var engine: MobileSynk?
    private let dir: URL
    private let documents: URL
    private var fileWatcher: DispatchSourceFileSystemObject?
    private var watchDebounce: DispatchWorkItem?

    init() {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        dir = support.appendingPathComponent("Synk", isDirectory: true)
        documents = FileProviderModel.librariesRoot()
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
            registerFileProviderDomain()
            startWatchingLibraries()
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
        signalFileProviderChanges()
    }

    // MARK: File Provider (decision 0011)

    /// Registers the Synk domain with the File Provider system. The
    /// system creates the disk location the domain is replicated into;
    /// the extension serves its contents from the app group container.
    /// Called once at startup; re-registering an existing domain just
    /// updates its display name, and the "disk location already exists"
    /// error is the expected outcome of that.
    func registerFileProviderDomain() {
        let domain = NSFileProviderDomain(identifier: FileProviderModel.domain, displayName: "Synk")
        NSFileProviderManager.add(domain) { [weak self] error in
            Task { @MainActor in
                guard let error else { return }
                let nsError = error as NSError
                if nsError.domain == NSCocoaErrorDomain,
                   nsError.code == 516 { // NSFileWriteFileExistsError: the domain's disk location already exists
                    return
                }
                self?.fileProviderError = error.localizedDescription
            }
        }
    }

    /// Tells the system to pull the latest changes from the extension's
    /// working-set enumerator, so the Files app's view of On My iPhone
    /// → Synk stays current. The app signals on every refresh and, while
    /// it runs, through a file watcher on the libraries folder; the
    /// extension computes the actual changes by diffing a persisted
    /// snapshot. iOS does not guarantee background execution, so the
    /// view is freshest while the app is open or has just been.
    func signalFileProviderChanges() {
        NSFileProviderManager.default.signalEnumerator(for: .workingSet) { [weak self] (error: Error?) in
            Task { @MainActor in
                if let error {
                    self?.fileProviderError = error.localizedDescription
                }
            }
        }
    }

    /// Watches the libraries folder so engine writes reach the Files app
    /// while the app is running.
    func startWatchingLibraries() {
        let root = FileProviderModel.librariesRoot()
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let fd = open(root.path, O_EVTONLY)
        guard fd >= 0 else { return }
        let source = DispatchSource.makeFileSystemObjectSource(
            fileDescriptor: fd, eventMask: .all, queue: .global(qos: .utility)
        )
        source.setEventHandler { [weak self] in
            self?.schedulePublish()
        }
        source.setCancelHandler { close(fd) }
        source.activate()
        fileWatcher = source
    }

    private func schedulePublish() {
        let work = DispatchWorkItem { [weak self] in
            Task { @MainActor in self?.signalFileProviderChanges() }
        }
        watchDebounce?.cancel()
        watchDebounce = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0, execute: work)
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

    /// Adds a preset's library in the app group container's Documents
    /// folder. The folder name is the preset's name, so Files shows
    /// On My iPhone → Synk → Obsidian, and the same app preset syncs
    /// with the Mac by ID.
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
