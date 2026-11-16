import SwiftUI

@main
struct SynkApp: App {
    @StateObject private var model = SynkModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(model)
                .frame(minWidth: 720, minHeight: 520)
                .task { await model.start() }
        }
        .windowStyle(.hiddenTitleBar)

        MenuBarExtra("Synk", systemImage: "arrow.triangle.2.circlepath") {
            MenuStatus()
                .environmentObject(model)
        }
        .menuBarExtraStyle(.window)
    }
}

@MainActor
final class SynkModel: NSObject, ObservableObject {
    @Published var status: SynkStatus?
    @Published var presets: [Preset] = []
    @Published var discovered: [DiscoveredApp] = []
    @Published var pair: PairOffer?
    @Published var error: String?
    @Published var daemonUp = false
    @Published var loginEnabled = false
    /// What happened most recently, newest first, from the event stream.
    @Published var events: [SynkEvent] = []
    @Published var showOnboarding = false

    private let daemon = Daemon()
    private var client: SynkClient?
    private var eventsTask: Task<Void, Never>?

    /// Brings the daemon up and starts following what it reports. Does
    /// nothing while running under XCTest: tests manage their own daemons,
    /// and the app must not touch anyone's real data folder from a test.
    func start() async {
        guard ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil else {
            return
        }
        do {
            let client = try await daemon.startIfNeeded()
            self.client = client
            daemonUp = true
            error = nil
            await refresh()
            followEvents(client)
        } catch {
            daemonUp = false
            self.error = error.localizedDescription
        }
    }

    private func followEvents(_ client: SynkClient) {
        eventsTask?.cancel()
        eventsTask = Task {
            do {
                for try await event in client.events() {
                    if Task.isCancelled { return }
                    events.insert(event, at: 0)
                    if events.count > 100 {
                        events.removeLast(events.count - 100)
                    }
                    await refresh()
                }
            } catch is CancellationError {
            } catch {
                // The daemon stopped; the next refresh says so.
                daemonUp = false
            }
        }
    }

    func refresh() async {
        guard let client else {
            await start()
            return
        }
        do {
            status = try await client.status()
            let cat = try await client.catalog()
            presets = cat.catalog
            discovered = cat.discovered
            daemonUp = true
            error = nil
            // Show onboarding if there are no libraries
            showOnboarding = status?.libraries.isEmpty ?? true
            // Check login item status
            do {
                loginEnabled = try await client.getLogin()
            } catch {
                loginEnabled = false
            }
        } catch {
            daemonUp = false
            self.error = error.localizedDescription
        }
    }

    /// Enables a preset. `path` is where the app's files live: given for
    /// in-place presets, or when the person picked a folder.
    func enable(_ preset: Preset, path: String? = nil) async {
        guard let client else { return }
        do {
            _ = try await client.enable(preset: preset.id, path: path, label: nil, id: nil)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func offerPair() async {
        guard let client else { return }
        do {
            pair = try await client.startPair()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func setName(_ name: String) async throws -> String {
        guard let client else {
            throw NSError(
                domain: "SynkModel", code: 1,
                userInfo: [NSLocalizedDescriptionKey: "Client not available"])
        }
        return try await client.setName(name)
    }

    /// Joins a device by reading its synk://pair link, from a QR or pasted.
    func join(link: String) async {
        guard let client else { return }
        guard let parsed = PairLink(link: link) else {
            error = "That isn't a Synk pairing code."
            return
        }
        do {
            try await client.joinPair(parsed)
            pair = nil
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func pause(_ library: LibraryState) async {
        guard let client else { return }
        do {
            _ = try await client.pause(libraryID: library.libraryID)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func resume(_ library: LibraryState) async {
        guard let client else { return }
        do {
            _ = try await client.resume(libraryID: library.libraryID)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func restoreDeletes(_ libraryID: String) async {
        guard let client else { return }
        do {
            _ = try await client.restoreDeletes(libraryID: libraryID)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func confirmDeletes(_ libraryID: String, paths: [String]) async {
        guard let client else { return }
        do {
            _ = try await client.confirmDeletes(libraryID: libraryID, paths: paths)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func toggleLogin(_ enable: Bool) {
        Task {
            do {
                if enable {
                    _ = try await client?.enableLogin()
                } else {
                    _ = try await client?.disableLogin()
                }
                await refresh()
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    // MARK: - Bonjour Discovery

    private var bonjourBrowser: NetServiceBrowser?
    private var bonjourAdvertiser: NetService?
    private var bonjourDiscoveryHandler: (([DiscoveredDevice]) -> Void)?

    func startBonjourDiscovery(_ handler: @escaping ([DiscoveredDevice]) -> Void) {
        bonjourDiscoveryHandler = handler

        guard let client else { return }

        // Advertise this device with its ID and address
        Task {
            do {
                let status = try await client.status()
                let deviceID = status.deviceId
                let listenAddr = status.listen ?? "0.0.0.0:0"
                let port = Int32(listenAddr.split(separator: ":").last ?? "0") ?? 0

                bonjourAdvertiser = NetService(
                    domain: "local.",
                    type: "_synk._tcp.",
                    name: "Synk-\(deviceID)",
                    port: port)
                bonjourAdvertiser?.delegate = self
                bonjourAdvertiser?.publish()
            } catch {
                // If we can't get status, advertise with a placeholder
                bonjourAdvertiser = NetService(domain: "local.", type: "_synk._tcp.", name: "Synk", port: 0)
                bonjourAdvertiser?.delegate = self
                bonjourAdvertiser?.publish()
            }
        }

        // Browse for other devices
        bonjourBrowser = NetServiceBrowser()
        bonjourBrowser?.delegate = self
        bonjourBrowser?.searchForServices(ofType: "_synk._tcp.", inDomain: "local.")
    }

    func pairWithDevice(_ deviceID: String, address: String) async {
        guard let client else { return }
        do {
            try await client.pairWithDeviceID(deviceID, address: address)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func stopBonjourDiscovery() {
        bonjourBrowser?.stop()
        bonjourAdvertiser?.stop()
        bonjourBrowser = nil
        bonjourAdvertiser = nil
    }
}

// MARK: - NetServiceBrowserDelegate

extension SynkModel: NetServiceBrowserDelegate {
    func netServiceBrowser(
        _ browser: NetServiceBrowser,
        didFind service: NetService,
        moreComing _: Bool
    ) {
        // Resolve the service synchronously
        _ = service.resolve(withTimeout: 5)
        guard let addresses = service.addresses,
              let address = addresses.first?.description else { return }
        // Use service name as device ID for now (TXT record handling is a follow-up)
        let deviceID = service.name.components(separatedBy: " ").first ?? "unknown"
        let name = service.name.hasPrefix("Synk") ? "Synk" : service.name
        let discovered = DiscoveredDevice(id: service.name, name: name, deviceID: deviceID, address: address)
        Task { @MainActor in
            bonjourDiscoveryHandler?([discovered])
        }
    }

    func netServiceBrowser(
        _ browser: NetServiceBrowser,
        didRemove service: NetService,
        moreComing _: Bool
    ) {
        // Device disappeared
    }

    func netServiceBrowserDidStopSearch(_ browser: NetServiceBrowser) {
        // Search stopped
    }
}

// MARK: - NetServiceDelegate

extension SynkModel: NetServiceDelegate {
    func netServiceDidPublish(_ service: NetService) {
        // Service is now advertising
    }

    func netService(_ service: NetService, didNotPublish _: [String: Any]) {
        // Failed to advertise
    }
}
