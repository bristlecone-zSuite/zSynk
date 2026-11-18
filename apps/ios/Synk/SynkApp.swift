import SwiftUI
import SynkCore
import UIKit
import Vision
import VisionKit

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
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = SynkModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(model)
                .task { await model.start() }
        }
    }
}

/// Registers the background task handlers at launch — BGTaskScheduler
/// requires registration before the app finishes launching, which is
/// earlier than any SwiftUI code runs (decision 0014).
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        BackgroundSync.register()
        return true
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
                } else if phase == .background {
                    // Ask for the later chances and use the short time
                    // iOS grants every backgrounding to sync what just
                    // changed (decision 0014).
                    BackgroundSync.submitRequests()
                    model.flushBeforeBackground()
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
