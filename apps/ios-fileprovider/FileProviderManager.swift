import Foundation
import FileProvider
import UniformTypeIdentifiers

/// The File Provider extension's principal class (decision 0011).
///
/// It conforms to `NSFileProviderReplicatedExtension`, the modern
/// protocol: the system asks it to enumerate folders, to serve item
/// metadata and file contents, and to report working-set changes.
///
/// The extension is a read-only view of the engine's libraries. It
/// never runs the engine and never writes into the synced folders; it
/// only reads them. Writes and deletions from Files are rejected.
///
/// Changes are computed by diffing a persisted snapshot against the
/// live tree when the system asks the working-set enumerator for
/// changes. The app signals the working set after the engine writes
/// files; the system then pulls the changes from here.
final class FileProviderManager: NSObject, NSFileProviderReplicatedExtension {
    private let root: URL
    private let stateURL: URL

    init(domain: NSFileProviderDomain) {
        self.root = FileProviderModel.librariesRoot()
        self.stateURL = FileProviderModel.stateFileURL()
        super.init()
    }

    func invalidate() {}

    // MARK: NSFileProviderEnumerating

    func enumerator(for containerItemIdentifier: NSFileProviderItemIdentifier,
                    request: NSFileProviderRequest) throws -> NSFileProviderEnumerator {
        if containerItemIdentifier == NSFileProviderItemIdentifier.workingSet {
            return SynkChangeEnumerator(root: root, stateURL: stateURL)
        }
        return SynkFolderEnumerator(root: root, folderID: containerItemIdentifier)
    }

    // MARK: Items and contents

    func item(for identifier: NSFileProviderItemIdentifier, request: NSFileProviderRequest,
              completionHandler: @escaping (NSFileProviderItem?, Error?) -> Void) -> Progress {
        let progress = Progress()
        if identifier == NSFileProviderItemIdentifier.rootContainer || identifier.rawValue == FileProviderModel.rootID {
            completionHandler(SynkFileProviderItem(item: FileProviderModel.rootItem(root: root)), nil)
            progress.totalUnitCount = 1
        progress.completedUnitCount = 1
            return progress
        }
        if let item = FileProviderModel.liveItem(forID: identifier.rawValue, root: root) {
            completionHandler(SynkFileProviderItem(item: item), nil)
        } else {
            completionHandler(nil, FileProviderModel.notFoundError(identifier))
        }
        progress.totalUnitCount = 1
        progress.completedUnitCount = 1
        return progress
    }

    func fetchContents(for identifier: NSFileProviderItemIdentifier, version: NSFileProviderItemVersion?,
                       request: NSFileProviderRequest,
                       completionHandler: @escaping (URL?, NSFileProviderItem?, Error?) -> Void) -> Progress {
        let progress = Progress()
        let url = FileProviderModel.url(forID: identifier.rawValue, root: root)
        if let item = FileProviderModel.liveItem(forID: identifier.rawValue, root: root), !item.isDirectory {
            completionHandler(url, SynkFileProviderItem(item: item), nil)
        } else {
            completionHandler(nil, nil, FileProviderModel.notFoundError(identifier))
        }
        progress.totalUnitCount = 1
        progress.completedUnitCount = 1
        return progress
    }

    // MARK: Read-only domain

    // The write callbacks carry the protocol's own signatures; the count
    // rule is disabled where the protocol demands the extra parameters.

    // swiftlint:disable:next function_parameter_count
    func createItem(basedOn template: NSFileProviderItem, fields: NSFileProviderItemFields, contents: URL?,
                    options: NSFileProviderCreateItemOptions, request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?)
                        -> Void) -> Progress {
        let progress = Progress()
        completionHandler(nil, fields, false, FileProviderModel.readOnlyError())
        progress.totalUnitCount = 1
        progress.completedUnitCount = 1
        return progress
    }

    // swiftlint:disable:next function_parameter_count
    func modifyItem(_ item: NSFileProviderItem, baseVersion: NSFileProviderItemVersion,
                    changedFields: NSFileProviderItemFields, contents: URL?,
                    options: NSFileProviderModifyItemOptions, request: NSFileProviderRequest,
                    completionHandler: @escaping (NSFileProviderItem?, NSFileProviderItemFields, Bool, Error?)
                        -> Void) -> Progress {
        let progress = Progress()
        completionHandler(nil, changedFields, false, FileProviderModel.readOnlyError())
        progress.totalUnitCount = 1
        progress.completedUnitCount = 1
        return progress
    }

    func deleteItem(identifier: NSFileProviderItemIdentifier, baseVersion: NSFileProviderItemVersion,
                    options: NSFileProviderDeleteItemOptions, request: NSFileProviderRequest,
                    completionHandler: @escaping (Error?) -> Void) -> Progress {
        let progress = Progress()
        completionHandler(FileProviderModel.deletionRejectedError())
        progress.totalUnitCount = 1
        progress.completedUnitCount = 1
        return progress
    }
}

/// Enumerates the children of one folder in the live tree. The root
/// container is synthetic: its only child is the domain's root item.
final class SynkFolderEnumerator: NSObject, NSFileProviderEnumerator {
    let enumeratedItemIdentifier: NSFileProviderItemIdentifier
    private let root: URL

    init(root: URL, folderID: NSFileProviderItemIdentifier) {
        self.root = root
        self.enumeratedItemIdentifier = folderID
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        var items: [NSFileProviderItem] = []
        if enumeratedItemIdentifier == NSFileProviderItemIdentifier.rootContainer {
            items.append(SynkFileProviderItem(item: FileProviderModel.rootItem(root: root)))
        } else {
            let url = FileProviderModel.url(forID: enumeratedItemIdentifier.rawValue, root: root)
            if let entries = try? FileManager.default.contentsOfDirectory(
                at: url, includingPropertiesForKeys: nil, options: [.skipsHiddenFiles]
            ) {
                for entry in entries.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
                    let item = FileProviderModel.makeItem(at: entry, parentID: enumeratedItemIdentifier.rawValue)
                    items.append(SynkFileProviderItem(item: item))
                }
            }
        }
        observer.didEnumerate(items)
        observer.finishEnumerating(upTo: nil)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        // Folder enumerations don't report changes; the working set does.
        observer.finishEnumeratingChanges(upTo: FileProviderModel.anchorNow(), moreComing: false)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(FileProviderModel.anchorNow())
    }
}

/// The working-set enumerator. When the system asks for changes, it
/// diffs the persisted snapshot against the live tree, reports the
/// difference, and stores the new snapshot.
final class SynkChangeEnumerator: NSObject, NSFileProviderEnumerator {
    let enumeratedItemIdentifier: NSFileProviderItemIdentifier = .workingSet
    private let root: URL
    private let stateURL: URL

    init(root: URL, stateURL: URL) {
        self.root = root
        self.stateURL = stateURL
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        // The working set is not browsed; changes are.
        observer.finishEnumerating(upTo: nil)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        let old = FileProviderModel.loadSnapshot(from: stateURL)
        let new = FileProviderModel.snapshot(librariesRoot: root)
        let report = FileProviderModel.report(old: old, new: new)
        if !report.upserted.isEmpty {
            observer.didUpdate(report.upserted)
        }
        if !report.removed.isEmpty {
            observer.didDeleteItems(withIdentifiers: report.removed)
        }
        try? FileProviderModel.saveSnapshot(new, to: stateURL)
        observer.finishEnumeratingChanges(upTo: FileProviderModel.anchorNow(), moreComing: false)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(FileProviderModel.anchorNow())
    }
}
