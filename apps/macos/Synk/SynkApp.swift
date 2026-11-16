import SwiftUI

@main
struct SynkApp: App {
    @StateObject private var model = SynkModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(model)
                .frame(minWidth: 720, minHeight: 520)
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
final class SynkModel: ObservableObject {
    @Published var status: SynkStatus?
    @Published var presets: [Preset] = []
    @Published var pair: PairOffer?
    @Published var error: String?
    @Published var daemonUp = false
    private let client = SynkClient()

    func refresh() async {
        do {
            status = try await client.status()
            let apps = try await client.apps()
            presets = apps.catalog
            daemonUp = true
            error = nil
        } catch {
            daemonUp = false
            self.error = "Synk is not running yet. Launching the local engine…"
        }
    }

    func enable(_ preset: Preset) async {
        do {
            try await client.enable(preset: preset.id, path: nil)
            await refresh()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func offerPair() async {
        do {
            pair = try await client.startPair()
        } catch {
            self.error = error.localizedDescription
        }
    }
}
