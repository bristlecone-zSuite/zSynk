import SwiftUI
import CoreImage.CIFilterBuiltins

// MARK: - Onboarding

/// The first-run wizard: name the device, pick apps, pair with another.
struct OnboardingView: View {
    @EnvironmentObject var model: SynkModel
    @State private var step = 0  // 0 = name, 1 = apps, 2 = pair
    @State private var deviceName = ""
    @State private var nameError: String?

    var body: some View {
        VStack(spacing: 0) {
            // Progress bar
            if step < 2 {
                ProgressView(value: Double(step) / 2.0)
                    .padding(.horizontal)
                    .padding(.top, 8)
            }

            Group {
                if step == 0 { nameStep() } else if step == 1 { appPickerStep() } else { pairStep() }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .navigationTitle("Welcome to Synk")
        .toolbar {
            if step > 0 && step < 2 {
                Button("Back") { step -= 1 }
            }
        }
    }

    // MARK: Steps

    @ViewBuilder
    func nameStep() -> some View {
        VStack(spacing: 20) {
            Image(systemName: "person.circle.fill")
                .font(.system(size: 64))
                .foregroundStyle(.secondary)
            Text("Name this device")
                .font(.title)
            Text("Give this Mac a name so you can tell your devices apart.")
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 400)

            TextField("e.g. Studio Mac", text: $deviceName)
                .textFieldStyle(.roundedBorder)
                .frame(maxWidth: 300)
                .onSubmit { saveName() }

            if let err = nameError {
                Text(err).foregroundStyle(.red).font(.caption)
            }

            Button("Continue") { saveName() }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .disabled(deviceName.trimmingCharacters(in: .whitespaces).isEmpty)
        }
        .padding()
    }

    @ViewBuilder
    func appPickerStep() -> some View {
        VStack(spacing: 16) {
            Text("What should we keep in sync?")
                .font(.title)
            Text("Pick the apps you actually write in. Synk will watch their data folders.")
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 500)

            ScrollView {
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 220))], spacing: 16) {
                    ForEach(model.presets) { p in
                        AppCard(preset: p)
                    }
                }
                .padding()
            }

            Button("Continue") { step = 2 }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
        }
    }

    @ViewBuilder
    func pairStep() -> some View {
        VStack(spacing: 16) {
            Text("Add another device")
                .font(.title)
            Text("On the other device, open Synk and scan this code — or pick a device on your network.")
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 500)

            if let pair = model.pair {
                VStack(spacing: 12) {
                    QRCodeView(text: pair.qr)
                        .frame(width: 240, height: 240)
                    Text(pair.code)
                        .font(.system(size: 36, weight: .bold, design: .monospaced))
                    Text("or paste a code from the other device")
                        .foregroundStyle(.secondary)
                    TextField("synk://pair?...", text: .constant(""))
                        .textFieldStyle(.roundedBorder)
                        .frame(maxWidth: 400)
                        .onSubmit { /* paste handling is a Phase 4 follow-up */ }
                }
                .padding()
            }

            Button("Show pairing code") {
                Task { await model.offerPair() }
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)

            Divider()
                .padding(.vertical, 4)

            Text("Or discover nearby")
                .font(.headline)
            BonjourDiscoveryView()

            Spacer()
        }
        .padding()
    }

    // MARK: Actions

    private func saveName() {
        let name = deviceName.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else {
            nameError = "Please enter a name."
            return
        }
        Task {
            do {
                _ = try await model.setName(name)
                await model.refresh()
                step = 1
            } catch {
                nameError = error.localizedDescription
            }
        }
    }
}

// MARK: - App Card

struct AppCard: View {
    let preset: Preset
    @EnvironmentObject var model: SynkModel
    @State private var enabled = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(preset.name).font(.headline)
            Text(preset.summary).font(.subheadline).foregroundStyle(.secondary)
            Text(preset.hint).font(.caption).foregroundStyle(.secondary)
            if let w = preset.warning {
                Text(w).font(.caption).foregroundStyle(.orange)
            }
            if enabled {
                Label("Enabled", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
                    .font(.caption)
            } else {
                Button("Keep in sync") {
                    Task {
                        await model.enable(preset)
                        enabled = true
                    }
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .padding()
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 16))
    }
}

// MARK: - QR Code View

/// Draws a QR code from text using CoreImage's built-in generator.
struct QRCodeView: View {
    let text: String
    @State private var image: Image?

    var body: some View {
        Group {
            if let image {
                image
                    .resizable()
                    .interpolation(.none)
            } else {
                ProgressView()
            }
        }
        .task {
            image = try? makeQR(text)
        }
    }

    private func makeQR(_ text: String) throws -> Image {
        guard let data = text.data(using: .utf8) else { return Image(decorative: "qrcode") }
        let filter = CIFilter(name: "CIQRCodeGenerator")!
        filter.setValue(data, forKey: "inputMessage")
        guard let output = filter.outputImage else { return Image(decorative: "qrcode") }
        let transform = CGAffineTransform(scaleX: 10, y: 10)
        let scaled = output.transformed(by: transform)
        let context = CIContext(options: [.useSoftwareRenderer: false])
        guard let cg = context.createCGImage(scaled, from: scaled.extent) else {
            return Image(decorative: "qrcode")
        }
        return Image(cg, scale: 1, orientation: .left, label: Text("QR code"))
    }

    private var qr: String { "CIQRCodeGenerator" }
}

// MARK: - Conflict Center

/// Shows held deletes per library and lets the user restore or confirm them.
struct ConflictCenter: View {
    @EnvironmentObject var model: SynkModel
    @State private var expanding: Set<String> = []

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Conflict Center")
                .font(.title)
            Text("Some files were deleted on one device while present on another. Review what happened.")
                .foregroundStyle(.secondary)

            if model.status?.libraries.isEmpty ?? true {
                Text("No libraries are configured yet.")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(heldLibraries, id: \.libraryID) { lib in
                    HeldDeleteSection(library: lib, isExpanded: binding(for: lib))
                }
            }
        }
        .padding()
    }

    private var heldLibraries: [LibraryState] {
        guard let s = model.status else { return [] }
        return s.libraries.filter { ($0.heldDeletes ?? []).isEmpty == false }
    }

    private func binding(for lib: LibraryState) -> Binding<Bool> {
        Binding {
            expanding.contains(lib.libraryID)
        } set: { value in
            if value { expanding.insert(lib.libraryID) } else { expanding.remove(lib.libraryID) }
        }
    }
}

struct HeldDeleteSection: View {
    let library: LibraryState
    @Binding var isExpanded: Bool
    @EnvironmentObject var model: SynkModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(library.label)
                    .font(.headline)
                Spacer()
                Text("\(library.heldDeletes?.count ?? 0) held deletes")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Button(isExpanded ? "Hide" : "Show") { isExpanded.toggle() }
                    .font(.caption)
            }

            if isExpanded, let deletes = library.heldDeletes {
                List(deletes, id: \.self) { path in
                    Text(path).font(.caption.monospaced())
                }
                .listStyle(.plain)
                .frame(maxHeight: 200)
                .padding(.top, 4)

                HStack(spacing: 12) {
                    Button("Restore all (put files back)") {
                        Task { await model.restoreDeletes(library.libraryID) }
                    }
                    .buttonStyle(.bordered)

                    Button("Confirm deletes (send to peer)") {
                        Task {
                            if let paths = library.heldDeletes {
                                await model.confirmDeletes(library.libraryID, paths: paths)
                            }
                        }
                    }
                    .buttonStyle(.borderedProminent)
                }
            }
        }
        .padding()
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 12))
    }
}

// MARK: - Bonjour Discovery

/// Discovers Synk services on the local network via mDNS/Bonjour.
struct BonjourDiscoveryView: View {
    @EnvironmentObject var model: SynkModel
    @State private var discovered: [DiscoveredDevice] = []
    @State private var browsing = false

    var body: some View {
        VStack(spacing: 8) {
            if !browsing {
                Button("Start browsing") {
                    startBrowsing()
                }
                .buttonStyle(.bordered)
            } else if discovered.isEmpty {
                HStack {
                    ProgressView()
                    Text("Looking for devices...")
                        .foregroundStyle(.secondary)
                        .font(.caption)
                }
                .padding(.vertical, 8)
            } else {
                ForEach(discovered) { device in
                    DiscoveredDeviceView(device: device)
                }
            }
        }
        .frame(maxWidth: .infinity)
    }

    private func startBrowsing() {
        browsing = true
        discovered = []
        model.startBonjourDiscovery { devices in
            discovered = devices
        }
    }
}

struct DiscoveredDevice: Identifiable {
    let id: String
    let name: String
    let deviceID: String
    let address: String
}

struct DiscoveredDeviceView: View {
    let device: DiscoveredDevice
    @EnvironmentObject var model: SynkModel
    @State private var pairing = false

    var body: some View {
        HStack {
            Image(systemName: "computer")
                .foregroundStyle(.secondary)
            Text(device.name)
                .font(.body)
            Spacer()
            if pairing {
                ProgressView()
                    .scaleEffect(0.8)
            } else {
                Button("Pair") {
                    pairing = true
                    Task {
                        await model.pairWithDevice(device.deviceID, address: device.address)
                        pairing = false
                    }
                }
                .buttonStyle(.bordered)
            }
        }
        .padding(.vertical, 4)
    }
}

// MARK: - Updated Panes

// NOTE: StatusPane is defined in Views.swift to avoid duplicate declarations.
