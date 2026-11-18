import Foundation
import FileProvider
import UniformTypeIdentifiers

/// The shared model for Synk's File Provider domain (decision 0011).
///
/// The engine syncs each library into the app group container's
/// `Documents`, and the File Provider extension exposes that folder as
/// a native Files location (On My iPhone → Synk).
///
/// The extension is the only process that talks to the File Provider
/// system: it lists folders, serves item metadata and file contents, and
/// computes changes by diffing a persisted snapshot against the live
/// tree. The app never builds File Provider items; it registers the
/// domain and signals the working set after the engine writes files,
/// and the system pulls the changes from the extension.
///
/// Everything here is plain Foundation plus the NSFileProvider item
/// adapter, so it compiles into both apps and is unit-testable on
/// macOS.
enum FileProviderModel {
    static let appGroup = "group.com.zyniker.synk"
    static let domain = NSFileProviderDomainIdentifier("synk")
    static let rootID = "synk"

    /// Where the engine syncs the libraries: the app group container's
    /// `Documents` when the group is available, otherwise the app's own
    /// `Documents` (the old Files exposure keeps working, and the File
    /// Provider simply shows nothing).
    static func librariesRoot() -> URL {
        if let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) {
            return group.appendingPathComponent("Documents", isDirectory: true)
        }
        return FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
    }

    // MARK: Item identifiers

    /// Item identifiers are path-like: `synk`, `synk/<library>`,
    /// `synk/<library>/<relative path>`. Each component is escaped so no
    /// file name can collide with the separator or with another name:
    /// `%` becomes `%25` and `/` becomes `%2F`. Every other character is
    /// kept as-is, so ordinary names (`note.md`, `2026-11-17.md`) stay
    /// readable and round-trip through `decode` unchanged.
    static func id(_ parent: String, _ name: String) -> String {
        parent + "/" + encode(name)
    }

    static func encode(_ component: String) -> String {
        var result = ""
        for char in component {
            switch char {
            case "%": result += "%25"
            case "/": result += "%2F"
            default: result.append(char)
            }
        }
        return result
    }

    static func decode(_ component: String) -> String {
        component.removingPercentEncoding ?? component
    }

    /// The file system location an item id points at.
    static func url(forID id: String, root: URL) -> URL {
        guard id != rootID else { return root }
        var url = root
        for part in id.split(separator: "/").dropFirst() {
            url = url.appendingPathComponent(decode(String(part)))
        }
        return url
    }

    /// The parent id of an item id. The root's parent is the system's
    /// root container.
    static func parentID(of id: String) -> String {
        guard id != rootID else { return NSFileProviderItemIdentifier.rootContainer.rawValue }
        return id.split(separator: "/").dropLast().joined(separator: "/")
    }

    // MARK: Snapshots and diffs

    /// One node of the synced tree.
    struct Item: Equatable, Codable {
        var id: String
        var parent: String
        var name: String
        var isDirectory: Bool
        var path: String
        var size: Int64
        var modified: Date
    }

    /// The full item tree under the libraries root. Each subdirectory of
    /// the root is a library. Hidden files are skipped: the engine keeps
    /// its own state outside the synced folders, and nothing hidden
    /// should ever surface in Files.
    static func snapshot(librariesRoot root: URL) -> [Item] {
        var items: [Item] = []
        guard let libraries = try? FileManager.default.contentsOfDirectory(
            at: root, includingPropertiesForKeys: nil, options: [.skipsHiddenFiles]
        ) else {
            return items
        }
        for library in libraries.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
            guard (try? library.resourceValues(forKeys: [.isDirectoryKey]))?.isDirectory == true else { continue }
            collect(at: library, parentID: rootID, into: &items)
        }
        return items
    }

    private static func collect(at url: URL, parentID: String, into items: inout [Item]) {
        items.append(makeItem(at: url, parentID: parentID))
        let item = items[items.count - 1]
        guard item.isDirectory,
              let entries = try? FileManager.default.contentsOfDirectory(
                  at: url, includingPropertiesForKeys: nil, options: [.skipsHiddenFiles]
              )
        else { return }
        for entry in entries.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
            collect(at: entry, parentID: item.id, into: &items)
        }
    }

    /// The item for one file system entry.
    static func makeItem(at url: URL, parentID: String) -> Item {
        let values = try? url.resourceValues(forKeys: [.isDirectoryKey, .fileSizeKey, .contentModificationDateKey])
        return Item(
            id: id(parentID, url.lastPathComponent),
            parent: parentID,
            name: url.lastPathComponent,
            isDirectory: values?.isDirectory ?? false,
            path: url.path,
            size: Int64(values?.fileSize ?? 0),
            modified: values?.contentModificationDate ?? Date(timeIntervalSince1970: 0)
        )
    }

    /// The domain's root item: the "Synk" folder that holds the
    /// libraries. Its parent is the system's root container.
    static func rootItem(root: URL) -> Item {
        let modified = (try? root.resourceValues(forKeys: [.contentModificationDateKey]))?.contentModificationDate
            ?? Date(timeIntervalSince1970: 0)
        return Item(id: rootID,
                    parent: NSFileProviderItemIdentifier.rootContainer.rawValue,
                    name: "Synk",
                    isDirectory: true,
                    path: root.path,
                    size: 0,
                    modified: modified)
    }

    /// The item for one id, read from the live file system. nil when
    /// the path does not exist.
    static func liveItem(forID id: String, root: URL) -> Item? {
        let url = url(forID: id, root: root)
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory) else { return nil }
        return makeItem(at: url, parentID: parentID(of: id))
    }

    /// What changed between two snapshots, by item id. A rename shows up
    /// as a remove plus an insert (v1).
    struct Diff {
        var inserted: [Item] = []
        var updated: [Item] = []
        var removed: [String] = []
        var isEmpty: Bool { inserted.isEmpty && updated.isEmpty && removed.isEmpty }
    }

    static func diff(old: [Item], new: [Item]) -> Diff {
        var diff = Diff()
        let oldByID = Dictionary(uniqueKeysWithValues: old.map { ($0.id, $0) })
        let newByID = Dictionary(uniqueKeysWithValues: new.map { ($0.id, $0) })
        for (id, item) in newByID where oldByID[id] == nil {
            diff.inserted.append(item)
        }
        for (id, item) in newByID {
            guard let oldItem = oldByID[id] else { continue }
            // A file changes when its size or mtime changes. A directory
            // does not: macOS bumps a folder's mtime whenever a child is
            // created or removed, and re-upserting the parent folder for
            // every child edit would churn the working set for no
            // user-visible change. A directory is updated only when its
            // kind flips (a file took the folder's id or vice versa).
            if oldItem.isDirectory != item.isDirectory ||
                (!item.isDirectory &&
                 (oldItem.size != item.size || oldItem.modified != item.modified)) {
                diff.updated.append(item)
            }
        }
        for (id, _) in oldByID where newByID[id] == nil {
            diff.removed.append(id)
        }
        diff.inserted.sort { $0.id < $1.id }
        diff.updated.sort { $0.id < $1.id }
        diff.removed.sort()
        return diff
    }

    /// What the extension reports to the system for a diff: the items to
    /// insert or update (full metadata) and the ids to remove.
    struct ChangeReport {
        var upserted: [NSFileProviderItem] = []
        var removed: [NSFileProviderItemIdentifier] = []
        var isEmpty: Bool { upserted.isEmpty && removed.isEmpty }
    }

    static func report(old: [Item], new: [Item]) -> ChangeReport {
        let diff = diff(old: old, new: new)
        var report = ChangeReport()
        report.upserted = (diff.inserted + diff.updated).map(SynkFileProviderItem.init)
        report.removed = diff.removed.map { NSFileProviderItemIdentifier($0) }
        return report
    }

    // MARK: Snapshot persistence

    /// Where the extension keeps the snapshot it last reported. It lives
    /// next to the libraries in the app group container, so only the
    /// extension (which owns it) and the app (which can see the group)
    /// can reach it.
    static func stateFileURL() -> URL {
        if let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) {
            return group.appendingPathComponent("fp-state.json")
        }
        return FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("fp-state.json")
    }

    static func saveSnapshot(_ items: [Item], to url: URL) throws {
        try JSONEncoder().encode(items).write(to: url, options: .atomic)
    }

    static func loadSnapshot(from url: URL) -> [Item] {
        guard let data = try? Data(contentsOf: url) else { return [] }
        return (try? JSONDecoder().decode([Item].self, from: data)) ?? []
    }

    /// A fresh sync anchor. The anchor is opaque to the system; the
    /// extension always diffs against its persisted snapshot, so the
    /// anchor only needs to be distinct between calls.
    static func anchorNow() -> NSFileProviderSyncAnchor {
        NSFileProviderSyncAnchor(Data(String(Int(Date().timeIntervalSince1970 * 1000)).utf8))
    }

    // MARK: Errors

    /// The messages the read-only domain shows when someone tries to
    /// change a library from Files.
    private enum Messages {
        static let readOnly =
            "Synk libraries are managed by the Synk app and can't be changed from Files."
        static let deletionRejected =
            "Synk libraries are managed by the Synk app and can't be removed from Files."
    }

    /// The File Provider error codes (NSFileProviderErrorDomain). The
    /// SDK's NS_ERROR_ENUM does not import a usable Swift type, so the
    /// raw values are named here.
    private enum FPErrorCode {
        static let noSuchItem = -1005
        static let deletionRejected = -1006
        static let cannotSynchronize = -2005
    }

    static func notFoundError(_ id: NSFileProviderItemIdentifier) -> NSError {
        NSError(domain: NSFileProviderErrorDomain, code: FPErrorCode.noSuchItem,
                userInfo: [NSLocalizedDescriptionKey: "The item no longer exists in Synk."])
    }

    /// The domain is a read-only view of the engine's libraries, so the
    /// extension refuses every write. `NSFileProviderErrorCannotSynchronize`
    /// tells the system not to retry until the extension or the item
    /// changes, which is exactly what a read-only domain wants.
    static func readOnlyError() -> NSError {
        NSError(domain: NSFileProviderErrorDomain, code: FPErrorCode.cannotSynchronize,
                userInfo: [NSLocalizedDescriptionKey: Messages.readOnly])
    }

    static func deletionRejectedError() -> NSError {
        NSError(domain: NSFileProviderErrorDomain, code: FPErrorCode.deletionRejected,
                userInfo: [NSLocalizedDescriptionKey: Messages.deletionRejected])
    }
}

/// The NSFileProviderItem adapter. Directories are flagged with
/// `contentType == .folder`; the system materializes file contents
/// through the extension's `fetchContents`, which reads the real file
/// from the app group container.
final class SynkFileProviderItem: NSObject, NSFileProviderItem {
    let itemIdentifier: NSFileProviderItemIdentifier
    let parentItemIdentifier: NSFileProviderItemIdentifier
    let filename: String
    let contentType: UTType
    let contentModificationDate: Date?
    let documentSize: NSNumber?

    /// The domain is a read-only view of the engine's libraries: the
    /// system may read file contents but never write them.
    var supportsFileCoordinatorRead: Bool { true }
    var supportsFileCoordinatorWrite: Bool { false }

    init(item: FileProviderModel.Item) {
        itemIdentifier = NSFileProviderItemIdentifier(item.id)
        parentItemIdentifier = NSFileProviderItemIdentifier(item.parent)
        filename = item.name
        if item.isDirectory {
            contentType = .folder
        } else {
            let ext = (item.name as NSString).pathExtension
            contentType = UTType(filenameExtension: ext) ?? .data
        }
        contentModificationDate = item.modified
        documentSize = NSNumber(value: item.isDirectory ? 0 : item.size)
    }
}
