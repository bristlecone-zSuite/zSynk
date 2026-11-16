package apps

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/safepath"
)

type Platform string

const (
	Mac Platform = "mac"
	IOS Platform = "ios"
	Any Platform = "any"
)

type Safety string

const (
	SafetyInPlace  Safety = "in-place" // watch the app's own folder
	SafetyManaged  Safety = "managed"  // Synk-owned folder; point the app at it
	SafetyExternal Safety = "external" // user-picked folder
)

type Preset struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Summary     string     `json:"summary"`
	Kind        string     `json:"kind"`
	Platforms   []Platform `json:"platforms"`
	Safety      Safety     `json:"safety"`
	PackageExts []string   `json:"packageExts,omitempty"` // package formats the app saves; every library syncs them as one unit
	Ignore      []string   `json:"ignore"`
	Discover    []string   `json:"discover"` // ~ paths; first existing wins for in-place
	Hint        string     `json:"hint"`
	Warning     string     `json:"warning,omitempty"`
	IOSHint     string     `json:"iosHint,omitempty"`
}

func Catalog() []Preset {
	return []Preset{
		{
			ID:        "obsidian",
			Name:      "Obsidian",
			Summary:   "Vaults of Markdown notes",
			Kind:      "notes",
			Platforms: []Platform{Mac, IOS},
			Safety:    SafetyExternal,
			Ignore: []string{
				".obsidian/cache/",
				".obsidian/workspace.json",
				".obsidian/workspace-mobile.json",
				".trash/",
			},
			Discover: []string{
				"~/Library/Mobile Documents/iCloud~md~obsidian/Documents",
				"~/Documents/Obsidian",
				"~/Obsidian",
			},
			Hint:    "Pick a vault folder. If it currently lives in iCloud, Synk will offer to move it to a local folder it owns — iCloud placeholders are refused, never treated as deletes.",
			IOSHint: "On iPhone/iPad, open the vault from Files → On My iPhone → Synk → Obsidian.",
			Warning: "Do not leave the vault in iCloud Drive while Synk is syncing it.",
		},
		{
			ID:          "ia-writer",
			Name:        "iA Writer",
			Summary:     "Markdown and .textbundle documents",
			Kind:        "notes",
			Platforms:   []Platform{Mac, IOS},
			Safety:      SafetyInPlace,
			PackageExts: []string{".textbundle"},
			Discover: []string{
				"~/Library/Containers/pro.writer.mac/Data/Documents",
				"~/Library/Mobile Documents/27N4MQEA55~pro~writer/Documents",
				"~/Documents/iA Writer",
			},
			Ignore:  []string{"index.db", "*.icloud"},
			Hint:    "Local iA Writer libraries sync in place. If you still use iCloud, move the library to On My Mac first.",
			IOSHint: "Add the Synk location in iA Writer’s Organizer → Locations.",
		},
		{
			ID:          "ulysses-local",
			Name:        "Ulysses (On My Mac)",
			Summary:     "The local Ulysses library, not iCloud",
			Kind:        "notes",
			Platforms:   []Platform{Mac, IOS},
			Safety:      SafetyInPlace,
			PackageExts: []string{".ulysses", ".ulgroup"},
			Discover: []string{
				"~/Library/Group Containers/X5AZV975AG.com.soulmen.shared/Ulysses/Documents/Library",
			},
			Hint:    "Only the local (On My Mac) library is supported. Ulysses External Folders are even better — add them as a custom folder.",
			Warning: "Quit Ulysses before the first sync. Sheet filenames are hashes; Synk still copies them intact.",
			IOSHint: "Enable Ulysses’ local library, or keep sheets in an External Folder that Synk owns.",
		},
		{
			ID:          "pages",
			Name:        "Pages",
			Summary:     "Pages documents (.pages packages)",
			Kind:        "iwork",
			Platforms:   []Platform{Mac, IOS},
			Safety:      SafetyManaged,
			PackageExts: []string{".pages"},
			Discover: []string{
				"~/Documents/Synk/Pages",
				"~/Documents/Pages",
			},
			Hint:    "Save or move documents into Synk’s Pages folder. Packages are copied as one unit so they never tear.",
			IOSHint: "Open files from Files → Synk → Pages, or use Open in Pages.",
			Warning: "Avoid syncing the iCloud Pages container. Apple’s package format corrupts if files inside are updated independently.",
		},
		{
			ID:          "keynote",
			Name:        "Keynote",
			Summary:     "Keynote presentations (.key packages)",
			Kind:        "iwork",
			Platforms:   []Platform{Mac, IOS},
			Safety:      SafetyManaged,
			PackageExts: []string{".key"},
			Discover: []string{
				"~/Documents/Synk/Keynote",
				"~/Documents/Keynote",
			},
			Hint:    "Keep presentations in Synk’s Keynote folder. Large media is content-addressed and only sent once.",
			IOSHint: "Files → Synk → Keynote.",
			Warning: "Close the presentation before expecting a sync; Synk waits until the package is idle.",
		},
		{
			ID:          "numbers",
			Name:        "Numbers",
			Summary:     "Numbers spreadsheets (.numbers packages)",
			Kind:        "iwork",
			Platforms:   []Platform{Mac, IOS},
			Safety:      SafetyManaged,
			PackageExts: []string{".numbers"},
			Discover: []string{
				"~/Documents/Synk/Numbers",
				"~/Documents/Numbers",
			},
			Hint:    "Keep spreadsheets in Synk’s Numbers folder.",
			IOSHint: "Files → Synk → Numbers.",
		},
		{
			ID:        "logseq",
			Name:      "Logseq",
			Summary:   "Logseq graphs",
			Kind:      "notes",
			Platforms: []Platform{Mac, IOS},
			Safety:    SafetyExternal,
			Ignore:    []string{".logseq/bak/", "logseq/bak/", ".recycle/", "logseq/.recycle/"},
			Discover:  []string{"~/Documents/Logseq", "~/logseq"},
			Hint:      "Pick the graph folder. Logseq iOS can open the same folder via Files.",
		},
		{
			ID:        "zettlr",
			Name:      "Zettlr",
			Summary:   "Zettlr workspaces",
			Kind:      "notes",
			Platforms: []Platform{Mac},
			Safety:    SafetyExternal,
			Discover:  []string{"~/Documents/Zettlr"},
			Hint:      "Pick the workspace directory.",
		},
		{
			ID:        "typora",
			Name:      "Typora",
			Summary:   "A folder of Markdown files",
			Kind:      "notes",
			Platforms: []Platform{Mac},
			Safety:    SafetyExternal,
			Discover:  []string{"~/Documents/Typora"},
			Hint:      "Typora has no fixed library — pick the folder you actually write in.",
		},
		{
			ID:          "scrivener",
			Name:        "Scrivener",
			Summary:     "Scrivener projects (.scriv packages)",
			Kind:        "writing",
			Platforms:   []Platform{Mac, IOS},
			Safety:      SafetyManaged,
			PackageExts: []string{".scriv"},
			Discover:    []string{"~/Documents/Synk/Scrivener", "~/Documents/Scrivener"},
			Hint:        "Projects are packages. Synk copies them atomically.",
			Warning:     "Close the project in Scrivener before the first sync.",
		},
		{
			ID:        "drafts-local",
			Name:      "Drafts (file export)",
			Summary:   "A folder Drafts writes Markdown into",
			Kind:      "notes",
			Platforms: []Platform{Mac, IOS},
			Safety:    SafetyManaged,
			Discover:  []string{"~/Documents/Synk/Drafts"},
			Hint:      "Drafts’ own database is not a folder. Point a Drafts Action at this folder, or use Drafts’ file bookmarks.",
		},
		{
			ID:        "bbedit",
			Name:      "BBEdit / TextEdit / Nova",
			Summary:   "Plain folders of documents",
			Kind:      "documents",
			Platforms: []Platform{Mac},
			Safety:    SafetyExternal,
			Discover:  []string{"~/Documents"},
			Hint:      "Any folder. Synk will not sync your entire home directory — pick a project or Documents subfolder.",
		},
		{
			ID:        "custom",
			Name:      "Any folder",
			Summary:   "A folder you choose",
			Kind:      "documents",
			Platforms: []Platform{Mac, IOS},
			Safety:    SafetyExternal,
			Hint:      "Use this for anything else. Packages (.pages, .key, .numbers, .textbundle, .scriv) are still treated as units.",
		},
	}
}

func ByID(id string) (Preset, bool) {
	for _, p := range Catalog() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

type Found struct {
	Preset Preset   `json:"preset"`
	Paths  []string `json:"paths"`
	// CloudPaths are folders found in iCloud Drive or another cloud
	// provider's folder. Synk won't sync those in place; the library has to
	// move to a local folder first.
	CloudPaths []string `json:"cloudPaths"`
}

func Discover() []Found {
	home, _ := os.UserHomeDir()
	out := []Found{}
	for _, p := range Catalog() {
		out = append(out, find(home, p))
	}
	return out
}

func find(home string, p Preset) Found {
	f := Found{Preset: p, Paths: []string{}, CloudPaths: []string{}}
	for _, d := range p.Discover {
		exp := expand(home, d)
		if st, err := os.Stat(exp); err != nil || !st.IsDir() {
			continue
		}
		if InCloud(home, exp) {
			f.CloudPaths = append(f.CloudPaths, exp)
		} else {
			f.Paths = append(f.Paths, exp)
		}
	}
	return f
}

// ErrNoFolder means there's no folder a preset's library can use by default.
var ErrNoFolder = errors.New("apps: no folder to sync")

// DefaultPath is the folder a library for presetID syncs when none is
// picked: Synk's own folder for managed presets, otherwise the first local
// folder found for the app.
func DefaultPath(presetID string) (string, error) {
	p, ok := ByID(presetID)
	if !ok {
		return "", fmt.Errorf("apps: unknown preset %q", presetID)
	}
	if p.Safety == SafetyManaged {
		return ManagedPath(presetID)
	}
	home, _ := os.UserHomeDir()
	f := find(home, p)
	switch {
	case len(f.Paths) > 0:
		return f.Paths[0], nil
	case len(f.CloudPaths) > 0:
		return "", fmt.Errorf("%w: %s is in iCloud Drive or a cloud storage folder, which Synk won't sync in place; move it to a local folder and pick that", ErrNoFolder, f.CloudPaths[0])
	default:
		return "", fmt.Errorf("%w: no %s folder found; pick one", ErrNoFolder, p.Name)
	}
}

// CloudRoots are where macOS keeps iCloud Drive and the folders of File
// Provider apps such as Dropbox, Google Drive, and OneDrive. Files there
// can be evicted at any time, and another sync engine owns them.
func CloudRoots(home string) []string {
	return []string{
		filepath.Join(home, "Library", "Mobile Documents"),
		filepath.Join(home, "Library", "CloudStorage"),
	}
}

// InCloud reports whether p is in one of CloudRoots, directly or through a
// symlink.
func InCloud(home, p string) bool {
	if home == "" {
		return false
	}
	real := safepath.Resolve(p)
	for _, root := range CloudRoots(home) {
		if safepath.Within(p, root) || safepath.Within(real, safepath.Resolve(root)) {
			return true
		}
	}
	return false
}

func expand(home, p string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return p
}

func ManagedPath(presetID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := presetID
	if p, ok := ByID(presetID); ok {
		name = p.Name
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Documents", "Synk", name), nil
	}
	return filepath.Join(home, "Synk", name), nil
}
