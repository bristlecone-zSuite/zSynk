# App presets

Synk hides folder hunting. Each preset knows where the app usually keeps files, which paths are packages, and what must never be treated as user data.

| Preset | Default posture | Notes |
| --- | --- | --- |
| Obsidian | User-picked vault | Refuse iCloud Drive vaults; move to a local folder. iOS opens `Files → Synk → Obsidian`. |
| iA Writer | In-place local library | `.textbundle` is atomic. Add the Synk folder as a Location on iOS. |
| Ulysses (On My Mac) | In-place local library | iCloud Ulysses is unsupported. External Folders are preferred. |
| Pages / Keynote / Numbers | Managed `~/Documents/Synk/…` | Never the iCloud container. Packages swapped as a whole. |
| Logseq, Zettlr, Typora | External folder | Pick the graph/workspace. |
| Scrivener | Managed folder | `.scriv` is a package; close the project for first sync. |
| Drafts | Managed folder | Drafts’ database is not a folder; point an action at Synk. |
| Any folder | User-picked | Packages still detected by extension. |

## iCloud

If a preset discovers `~/Library/Mobile Documents/…`, the UI warns and offers to **move** the library to a Synk-owned folder. Syncing *through* iCloud is the bug we are replacing: placeholders, eviction, and package tearing.

## Adding a preset

Add an entry to `internal/apps/catalog.go`. Keep ignore lists conservative (caches, workspaces that fight, trash). When in doubt, sync the file — extras are safer than missing notes.
