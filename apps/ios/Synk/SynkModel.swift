// The model owns the embedded engine and everything the UI reads:
// status, the catalog, the libraries, pairing, and the File Provider
// domain. All engine calls hop through the main actor; the engine
// itself runs on its own threads (decision 0008).

import Foundation
import FileProvider
import UIKit

@MainActor
final class SynkModel: ObservableObject {
    @Published private(set) var status: SynkStatus?
    @Published private(set) var catalog: [Preset] = []
    @Published private(set) var error: String?
    @Published private(set) var pairMessage: String?
    @Published private(set) var libraryMessage: String?
    @Published private(set) var fileProviderError: String?

    private var engine: MobileSynk?
    /// Background tasks and the flush-on-backgrounding pass hand their
    /// work to the model through this holder (decision 0014).
    private var backgroundTaskID: UIBackgroundTaskIdentifier = .invalid
    private let dir: URL
    private let documents: URL
    private var fileWatcher: DispatchSourceFileSystemObject?
    private var watchDebounce: DispatchWorkItem?

    init() {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        dir = support.appendingPathComponent("Synk", isDirectory: true)
        documents = FileProviderModel.librariesRoot()
        BackgroundSync.model = self
    }

    /// Opens the engine if this process has not opened it yet. A
    /// background task launch starts without the UI having run, so the
    /// task body may be the first thing that opens the engine.
    func ensureRunning() async {
        guard engine == nil else { return }
        await start()
    }

    /// One sync pass for the background tasks: sync every library once
    /// and refresh the status, then report whether it succeeded. The
    /// task's deadline governs how long this may run; the expiration
    /// handler cancels this work, and the pass ends at the next
    /// opportunity after that.
    func runBackgroundPass() async -> Bool {
        guard let engine else { return false }
        do {
            try engine.syncNow()
        } catch {
            self.error = error.localizedDescription
            return false
        }
        await refresh()
        return true
    }

    /// Called when the user leaves the app: hold the process alive for
    /// the short time iOS grants every backgrounding and use it to sync
    /// whatever changed just before. The first of the pass finishing or
    /// the deadline firing ends the task; the second call is a no-op.
    func flushBeforeBackground() {
        guard engine != nil, backgroundTaskID == .invalid else { return }
        backgroundTaskID = UIApplication.shared.beginBackgroundTask(withName: "synk-flush") { [weak self] in
            Task { @MainActor in self?.endBackgroundFlush() }
        }
        Task {
            if let engine { try? engine.syncNow() }
            await refresh()
            endBackgroundFlush()
        }
    }

    private func endBackgroundFlush() {
        guard backgroundTaskID != .invalid else { return }
        let id = backgroundTaskID
        backgroundTaskID = .invalid
        UIApplication.shared.endBackgroundTask(id)
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
