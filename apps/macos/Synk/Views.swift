import SwiftUI

struct RootView: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        Group {
            if model.showOnboarding {
                OnboardingView()
            } else {
                NavigationSplitView {
                    List {
                        NavigationLink("Status") { StatusPane() }
                        NavigationLink("Apps") { AppsPane() }
                        NavigationLink("Devices") { DevicesPane() }
                        if hasConflicts {
                            NavigationLink("Conflicts") { ConflictCenter() }
                        }
                        Divider()
                        Section("Settings") {
                            Toggle("Start at login", isOn: Binding(
                                get: { model.loginEnabled },
                                set: { model.toggleLogin($0) }
                            ))
                        }
                    }
                    .navigationTitle("Synk")
                    .listStyle(.sidebar)
                } detail: {
                    StatusPane()
                }
            }
        }
        .task { await model.refresh() }
    }

    private var hasConflicts: Bool {
        guard let s = model.status else { return false }
        return s.libraries.contains { !($0.heldDeletes?.isEmpty ?? true) }
    }
}

struct StatusPane: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Your documents, on every device.")
                .font(.largeTitle.weight(.semibold))
            if let s = model.status {
                Label(s.name, systemImage: "desktopcomputer")
                    .font(.title2)
                Text(s.deviceId)
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(.secondary)
                if s.libraries.isEmpty {
                    Text("Nothing is syncing yet. Pick the apps you actually write in.")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(s.libraries) { lib in
                        HStack {
                            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                            VStack(alignment: .leading) {
                                Text(lib.label).font(.headline)
                                Text("\(lib.files) items · \(lib.path)")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            } else {
                Text(model.error ?? "Starting…")
            }
            Spacer()
        }
        .padding(28)
    }
}

struct AppsPane: View {
    @EnvironmentObject var model: SynkModel
    @State private var configState: ConfigState?
    @State private var presentingImporter = false

    var body: some View {
        ScrollView {
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 220))], spacing: 16) {
                ForEach(model.discovered) { app in
                    AppCard(discovered: app, configState: $configState, presentingImporter: $presentingImporter)
                }
            }
            .padding()
        }
        .navigationTitle("What should we keep in sync?")
        .fileImporter(
            isPresented: $presentingImporter,
            allowedContentTypes: [.folder],
            allowsMultipleSelection: false
        ) { result in
            if case .success(let urls) = result,
               let url = urls.first,
               var cfg = configState {
                cfg.localPath = url.path
                configState = cfg
                Task { await model.enable(cfg.preset, path: cfg.localPath) }
                configState = nil
            } else {
                configState = nil
            }
        }
    }
}

/// Holds the state for configuring a discovered app with a local path.
struct ConfigState: Identifiable {
    let id = UUID()
    let preset: Preset
    var localPath: String?
}

struct DevicesPane: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Add a Mac or iPhone")
                .font(.title)
            Text("On the other device, tap Add device and scan this code — or type it.")
                .foregroundStyle(.secondary)
            Text("That's the entire pairing process.")
                .foregroundStyle(.secondary)
            if let pair = model.pair {
                Text(pair.code)
                    .font(.system(size: 40, weight: .bold, design: .monospaced))
                Text("Token \(pair.token)")
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
            }
            Button("Show a pairing code") {
                Task { await model.offerPair() }
            }
            .buttonStyle(.borderedProminent)
            if let s = model.status {
                ForEach(s.peers) { p in
                    Label(p.name, systemImage: "iphone")
                }
            }
            Spacer()
        }
        .padding(28)
    }
}

struct MenuStatus: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Synk").font(.headline)
            if let s = model.status {
                Text(s.libraries.isEmpty ? "Pick apps to sync"
                    : "\(s.libraries.count) libraries · \(s.peers.count) devices")
            } else {
                Text("Engine idle")
            }
            Button("Refresh") { Task { await model.refresh() } }
        }
        .padding()
        .frame(width: 280)
        .task { await model.refresh() }
    }
}
