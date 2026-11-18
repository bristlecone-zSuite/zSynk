import XCTest
import FileProvider
import UniformTypeIdentifiers
@testable import Synk

/// The one spelling of a path: what the file system answers for it.
/// macOS reports temporary directories as /var/folders/... but
/// enumerates their contents as /private/var/folders/...; realpath(3)
/// resolves both to the same real path.
private func realpath(_ path: String) -> String {
    path.withCString { p in
        guard let resolved = realpath(p, nil) else { return path }
        return String(cString: resolved)
    }
}

/// The File Provider model (decision 0011): the snapshot of the synced
/// tree, the diff between snapshots, the change report the extension
/// serves, and the item identifiers. These run on macOS against a
/// fixture directory, so the logic the extension and the app share is
/// tested without a simulator.
final class FileProviderModelTests: XCTestCase {
    private var root: URL!

    override func setUpWithError() throws {
        root = FileManager.default.temporaryDirectory
            .appendingPathComponent("synk-fp-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: root)
    }

    private func makeTree() throws {
        let obsidian = root.appendingPathComponent("Obsidian", isDirectory: true)
        try FileManager.default.createDirectory(at: obsidian, withIntermediateDirectories: true)
        try "hello".write(to: obsidian.appendingPathComponent("note.md"), atomically: true, encoding: .utf8)
        let folder = obsidian.appendingPathComponent("Daily", isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        try "2026-11-17".write(to: folder.appendingPathComponent("2026-11-17.md"), atomically: true, encoding: .utf8)
        let keynote = root.appendingPathComponent("Keynote", isDirectory: true)
        try FileManager.default.createDirectory(at: keynote, withIntermediateDirectories: true)
        try "deck".write(to: keynote.appendingPathComponent("deck.key"), atomically: true, encoding: .utf8)
    }

    func testSnapshotWalksTheTree() throws {
        try makeTree()
        let items = FileProviderModel.snapshot(librariesRoot: root)
        let byID = Dictionary(uniqueKeysWithValues: items.map { ($0.id, $0) })

        XCTAssertEqual(items.count, 6, "expected the two libraries, one folder and three files: \(items.map(\.id))")
        XCTAssertEqual(byID["synk/Obsidian"]?.isDirectory, true)
        XCTAssertEqual(byID["synk/Keynote"]?.isDirectory, true)
        XCTAssertEqual(byID["synk/Obsidian"]?.parent, FileProviderModel.rootID)
        XCTAssertEqual(byID["synk/Obsidian/note.md"]?.parent, "synk/Obsidian")
        XCTAssertEqual(byID["synk/Obsidian/Daily/2026-11-17.md"]?.parent, "synk/Obsidian/Daily")
        XCTAssertEqual(byID["synk/Obsidian/note.md"]?.size, 5)
        XCTAssertEqual(byID["synk/Obsidian/note.md"]?.name, "note.md")
        // The stored path is the file's real path: the extension serves
        // content straight from it. Enumerated URLs canonicalize the
        // spelling (/var/folders/... becomes /private/var/...), so
        // compare both sides through realpath(3), the one true spelling.
        XCTAssertEqual(byID["synk/Obsidian/note.md"]?.path,
                       realpath(root.appendingPathComponent("Obsidian/note.md").path))
    }

    func testSnapshotSkipsHiddenFiles() throws {
        try makeTree()
        let obsidian = root.appendingPathComponent("Obsidian", isDirectory: true)
        try "secret".write(to: obsidian.appendingPathComponent(".hidden"), atomically: true, encoding: .utf8)
        let items = FileProviderModel.snapshot(librariesRoot: root)
        XCTAssertFalse(items.contains { $0.name == ".hidden" }, "hidden files must not surface in Files")
    }

    func testDiffFindsInsertsUpdatesAndRemovals() throws {
        try makeTree()
        let before = FileProviderModel.snapshot(librariesRoot: root)

        // Insert, update, remove. The insert is a new directory with a
        // file in it: the File Provider needs the parent item before the
        // child, so the diff must report both, parent first.
        let obsidian = root.appendingPathComponent("Obsidian", isDirectory: true)
        let inbox = root.appendingPathComponent("Inbox", isDirectory: true)
        try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
        try "new".write(to: obsidian.appendingPathComponent("new.md"), atomically: true, encoding: .utf8)
        try "sorted".write(to: inbox.appendingPathComponent("inbox.md"), atomically: true, encoding: .utf8)
        let note = obsidian.appendingPathComponent("note.md")
        try "hello, updated".write(to: note, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes(
            [.modificationDate: Date(timeIntervalSinceNow: 3600)], ofItemAtPath: note.path)
        try FileManager.default.removeItem(at: root.appendingPathComponent("Keynote"))

        let after = FileProviderModel.snapshot(librariesRoot: root)
        let diff = FileProviderModel.diff(old: before, new: after)

        XCTAssertEqual(diff.inserted.map(\.id),
                       ["synk/Inbox", "synk/Inbox/inbox.md", "synk/Obsidian/new.md"],
                       "a new folder must be reported before its children")
        XCTAssertEqual(diff.updated.map(\.id), ["synk/Obsidian/note.md"],
                       "a folder whose mtime changed is not an update")
        XCTAssertTrue(diff.removed.contains("synk/Keynote"), "the removed library must be in the diff: \(diff.removed)")
        XCTAssertTrue(diff.removed.contains("synk/Keynote/deck.key"))
    }

    func testDiffIsEmptyWhenNothingChanged() throws {
        try makeTree()
        let before = FileProviderModel.snapshot(librariesRoot: root)
        let after = FileProviderModel.snapshot(librariesRoot: root)
        XCTAssertTrue(FileProviderModel.diff(old: before, new: after).isEmpty)
    }

    func testReportBuildsUpsertsAndRemovals() throws {
        try makeTree()
        let before = FileProviderModel.snapshot(librariesRoot: root)
        let obsidian = root.appendingPathComponent("Obsidian", isDirectory: true)
        try "new".write(to: obsidian.appendingPathComponent("new.md"), atomically: true, encoding: .utf8)
        try FileManager.default.removeItem(at: root.appendingPathComponent("Keynote"))
        let after = FileProviderModel.snapshot(librariesRoot: root)

        let report = FileProviderModel.report(old: before, new: after)
        XCTAssertEqual(report.upserted.map { $0.itemIdentifier.rawValue }, ["synk/Obsidian/new.md"])
        XCTAssertTrue(report.removed.contains(NSFileProviderItemIdentifier("synk/Keynote")),
                      "the removed library must be reported: \(report.removed.map(\.rawValue))")
        XCTAssertTrue(report.removed.contains(NSFileProviderItemIdentifier("synk/Keynote/deck.key")))
    }

    func testReportIsEmptyWhenNothingChanged() throws {
        try makeTree()
        let before = FileProviderModel.snapshot(librariesRoot: root)
        let after = FileProviderModel.snapshot(librariesRoot: root)
        let report = FileProviderModel.report(old: before, new: after)
        XCTAssertTrue(report.upserted.isEmpty)
        XCTAssertTrue(report.removed.isEmpty)
    }

    func testIDsRoundTripForTrickyNames() throws {
        let tricky = "my notes (1) 100%.md"
        let encoded = FileProviderModel.encode(tricky)
        XCTAssertFalse(encoded.contains("/"), "the encoding must not produce separators: \(encoded)")
        XCTAssertEqual(FileProviderModel.decode(encoded), tricky)

        // A literal percent sign in a name cannot collide with an
        // encoded separator.
        let literal = FileProviderModel.id("synk", "a%2Fb")
        let nested = FileProviderModel.id("synk", "a") + "/" + FileProviderModel.encode("b")
        XCTAssertNotEqual(literal, nested, "a%2Fb (literal) and a/b (nested) must not collide")
        XCTAssertEqual(FileProviderModel.decode(String(literal.split(separator: "/").last!)), "a%2Fb")
    }

    func testItemAdaptersCarryTheRightMetadata() throws {
        try makeTree()
        let items = FileProviderModel.snapshot(librariesRoot: root)
        let note = items.first { $0.id == "synk/Obsidian/note.md" }
        XCTAssertNotNil(note)
        let adapter = SynkFileProviderItem(item: note!)
        XCTAssertEqual(adapter.filename, "note.md")
        XCTAssertEqual(adapter.parentItemIdentifier.rawValue, "synk/Obsidian")
        XCTAssertEqual(adapter.documentSize, NSNumber(value: 5))
        // .md maps to the platform's markdown type (macOS 14), which is
        // what Files shows the note as — not plain text.
        XCTAssertEqual(adapter.contentType.identifier, "net.daringfireball.markdown")
        XCTAssertTrue(adapter.supportsFileCoordinatorRead)
        XCTAssertFalse(adapter.supportsFileCoordinatorWrite)

        let library = items.first { $0.id == "synk/Obsidian" }!
        let folderAdapter = SynkFileProviderItem(item: library)
        XCTAssertEqual(folderAdapter.contentType, UTType.folder,
                       "folders are identified by contentType, not isDirectory")
        XCTAssertEqual(folderAdapter.documentSize, NSNumber(value: 0))
    }

    func testLiveItemPointsAtTheRealFile() throws {
        try makeTree()
        let note = FileProviderModel.liveItem(forID: "synk/Obsidian/note.md", root: root)
        XCTAssertNotNil(note, "a live item must be found for an existing file")
        XCTAssertEqual(note?.size, 5)
        let url = FileProviderModel.url(forID: "synk/Obsidian/note.md", root: root)
        XCTAssertTrue(FileManager.default.fileExists(atPath: url.path), "the id must resolve to the real file")
        XCTAssertNil(FileProviderModel.liveItem(forID: "synk/Obsidian/missing.md", root: root))
    }

    func testPairLinkParsing() {
        let link = "synk://pair?v=2&code=DEADBEEF&token=***&fp=ABCD1234&addr=192.168.1.10%3A7421"
        let parsed = PairLink(link: link)
        XCTAssertNotNil(parsed, "a well-formed link must parse")
        XCTAssertEqual(parsed?.code, "DEADBEEF")
        XCTAssertEqual(parsed?.addrs, ["192.168.1.10:7421"])
        XCTAssertNil(PairLink(link: "https://example.com"), "a plain URL is not a pairing link")
        XCTAssertNil(PairLink(link: "garbage"))
        XCTAssertNil(PairLink(link: "synk://pair?code=only-a-code"), "a link missing token/fingerprint must not parse")
    }
}
