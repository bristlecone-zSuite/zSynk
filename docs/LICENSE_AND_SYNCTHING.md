# License and Syncthing

Synk’s original source is Apache-2.0.

## Can we use Syncthing?

Yes. Syncthing is Mozilla Public License 2.0. MPL is a **file-level** weak copyleft:

- You may combine Syncthing with a larger work (including a commercial app).
- You must keep Syncthing’s own files (and any edits to those files) under MPL-2.0.
- Recipients must be able to obtain that MPL source.
- Your new files (this repository) can stay Apache-2.0 or proprietary.

That is a clean legal basis for wrapping Syncthing.

## Why this repo does not vendor Syncthing *as the engine*

Syncthing is an excellent P2P folder replicator. It is not, by itself, a product for:

1. **Apple document packages.** Pages, Keynote, Numbers, iA Writer `.textbundle`, Scrivener `.scriv`, and Ulysses sheets are directory bundles. Syncing their interior files independently is a known way to produce documents that the host app will not open. Synk hashes and commits each package as one unit.
2. **iOS.** There is no official Syncthing iOS client. Third-party ports cannot run continuously. Background execution, File Provider, and “open this vault in Obsidian” need a first-party companion, not a generic folder daemon.
3. **iCloud placeholders.** A `.icloud` stub is not a delete. Treating eviction as deletion is how people lose vaults. Synk marks placeholders incomplete and refuses to propagate a delete from them.
4. **A paranoid version vault.** Before every overwrite or delete, Synk snapshots the previous bytes into a content-addressed vault. Conflicts are deterministic and never drop a side.
5. **App presets.** The whole point is that the user clicks Obsidian / Ulysses / Keynote and never sees rsync, folder IDs, or ignore syntax.

Syncthing remains a **future optional transport** for bulk Mac-to-Mac copies (NAT traversal and public relays already work). If we bundle it, it will live in a separate tree or binary, MPL notices will ship in `NOTICE`, and any patched Syncthing files will be published. Our reconcile/apply layer still sits on top so packages and placeholders stay safe.

rsync (GPL) is **not** used as a library for the same reason plus license gravity. The protocol here is original.
