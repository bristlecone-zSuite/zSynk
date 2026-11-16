import FileProvider
import UniformTypeIdentifiers

/// File Provider extension surface. Libraries appear under Files → On My iPhone → Synk.
/// The enumerator should read the local replica the engine maintains in the app container
/// (`Application Support/Synk/files/<library-id>/`).
final class Enumerator: NSObject, NSFileProviderEnumerator {
    let enumeratedItemIdentifier: NSFileProviderItemIdentifier

    init(enumeratedItemIdentifier: NSFileProviderItemIdentifier) {
        self.enumeratedItemIdentifier = enumeratedItemIdentifier
    }

    func invalidate() {}

    func enumerateItems(for observer: NSFileProviderEnumerationObserver, startingAt page: NSFileProviderPage) {
        observer.finishEnumerating(upTo: nil)
    }

    func enumerateChanges(for observer: NSFileProviderChangeObserver, from anchor: NSFileProviderSyncAnchor) {
        observer.finishEnumeratingChanges(upTo: anchor, moreComing: false)
    }

    func currentSyncAnchor(completionHandler: @escaping (NSFileProviderSyncAnchor?) -> Void) {
        completionHandler(NSFileProviderSyncAnchor("synk".data(using: .utf8)!))
    }
}

final class SynkItem: NSObject, NSFileProviderItem {
    let itemIdentifier: NSFileProviderItemIdentifier
    let parentItemIdentifier: NSFileProviderItemIdentifier
    let filename: String
    let contentType: UTType

    init(id: String, parent: NSFileProviderItemIdentifier, name: String, type: UTType) {
        itemIdentifier = NSFileProviderItemIdentifier(id)
        parentItemIdentifier = parent
        filename = name
        contentType = type
    }
}
