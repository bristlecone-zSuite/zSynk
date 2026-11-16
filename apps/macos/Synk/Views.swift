import SwiftUI

struct RootView: View {
    @EnvironmentObject var model: SynkModel

    var body: some View {
        NavigationSplitView {
            List {
                NavigationLink("Status") { StatusPane() }
                NavigationLink("Apps") { AppsPane() }
                NavigationLink("Devices") { DevicesPane() }
            }
            .navigationTitle("Synk")
            .listStyle(.sidebar)
        } detail: {
            StatusPane()
        }
        .task { await model.refresh() }
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

    var body: some View {
        ScrollView {
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 220))], spacing: 16) {
                ForEach(model.presets) { p in
                    VStack(alignment: .leading, spacing: 8) {
                        Text(p.name).font(.headline)
                        Text(p.summary).font(.subheadline).foregroundStyle(.secondary)
                        Text(p.hint).font(.caption).foregroundStyle(.secondary)
                        Button("Keep this in sync") {
                            Task { await model.enable(p) }
                        }
                        .buttonStyle(.borderedProminent)
                    }
                    .padding()
                    .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 16))
                }
            }
            .padding()
        }
        .navigationTitle("What should we keep in sync?")
    }
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
