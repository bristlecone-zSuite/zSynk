import SwiftUI
import UIKit

@main
struct SynkIOSApp: App {
    @StateObject private var model = IOSModel()

    var body: some Scene {
        WindowGroup {
            IOSRoot()
                .environmentObject(model)
        }
    }
}

@MainActor
final class IOSModel: ObservableObject {
    @Published var step = 0
    @Published var deviceName = UIDevice.current.name
    @Published var selected: Set<String> = []
    @Published var code = ""
    @Published var statusText = "Ready when you are."

    let presets: [(id: String, name: String, summary: String)] = [
        ("obsidian", "Obsidian", "Your vault, in Files"),
        ("ia-writer", "iA Writer", "Markdown and textbundles"),
        ("ulysses-local", "Ulysses", "On My iPhone library"),
        ("pages", "Pages", "Documents as packages"),
        ("keynote", "Keynote", "Presentations as packages"),
        ("numbers", "Numbers", "Spreadsheets as packages"),
    ]

    func finish() {
        statusText = "Synk will keep \(selected.count) apps in this phone’s Files folder. Open them from Obsidian, Writer, or iWork via On My iPhone → Synk."
        step = 3
    }
}

struct IOSRoot: View {
    @EnvironmentObject var model: IOSModel

    var body: some View {
        TabView(selection: $model.step) {
            welcome.tag(0)
            apps.tag(1)
            pair.tag(2)
            done.tag(3)
        }
        .tabViewStyle(.page)
        .background(Color(uiColor: .systemBackground))
    }

    var welcome: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text("Synk")
                .font(.largeTitle.bold())
            Text("Your notes and documents, on this iPhone, without iCloud.")
                .font(.title3)
                .foregroundStyle(.secondary)
            TextField("This iPhone is called", text: $model.deviceName)
                .textFieldStyle(.roundedBorder)
            Button("Continue") { model.step = 1 }
                .buttonStyle(.borderedProminent)
            Spacer()
        }
        .padding(28)
    }

    var apps: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text("What should live on this phone?")
                    .font(.title2.bold())
                ForEach(model.presets, id: \.id) { p in
                    Button {
                        if model.selected.contains(p.id) {
                            model.selected.remove(p.id)
                        } else {
                            model.selected.insert(p.id)
                        }
                    } label: {
                        HStack {
                            VStack(alignment: .leading) {
                                Text(p.name).font(.headline)
                                Text(p.summary).font(.caption).foregroundStyle(.secondary)
                            }
                            Spacer()
                            if model.selected.contains(p.id) {
                                Image(systemName: "checkmark.circle.fill")
                            }
                        }
                        .padding()
                        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 14))
                    }
                    .buttonStyle(.plain)
                }
                Button("Continue") { model.step = 2 }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.selected.isEmpty)
            }
            .padding(28)
        }
    }

    var pair: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Add a Mac")
                .font(.title.bold())
            Text("On the Mac, click Add device. Type the code here, or scan the QR.")
                .foregroundStyle(.secondary)
            TextField("XXXX-XXXX", text: $model.code)
                .textInputAutocapitalization(.characters)
                .font(.title.monospaced())
            Button("Pair") { model.finish() }
                .buttonStyle(.borderedProminent)
                .disabled(model.code.count < 8)
            Spacer()
        }
        .padding(28)
    }

    var done: some View {
        VStack(alignment: .leading, spacing: 16) {
            Image(systemName: "checkmark.seal.fill")
                .font(.system(size: 48))
                .foregroundStyle(.green)
            Text("That’s it.")
                .font(.largeTitle.bold())
            Text(model.statusText)
            Text("Open Files → On My iPhone → Synk, then open that folder from Obsidian or iA Writer. Synk catches up whenever you open this app, and whenever iOS lets it run in the background.")
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(28)
    }
}
