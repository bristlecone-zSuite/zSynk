package ignore

import "testing"

func TestMatch(t *testing.T) {
	m := Compile(append(GlobalPatterns(), ".obsidian/cache/", "*.tmp", "node_modules/", "logseq/bak/"))
	cases := []struct {
		path  string
		dir   bool
		match bool
	}{
		{"note.md", false, false},
		{".DS_Store", false, true},
		{"foo/bar.icloud", false, true},
		{".obsidian/cache/x", true, true},
		{".obsidian/cache", true, true},
		{".obsidian/cache", false, false},
		{".obsidian/workspace.json", false, false},
		{"scratch.tmp", false, true},
		{"plugins/node_modules/x", true, true},
		{"plugins/node_modules", true, true},
		{"plugins/node_modules", false, false},
		{"node_modules", false, false},
		{"logseq/bak/2026.md", false, true},
		{"Projects/logseq/bak/x.md", false, false},
		{".synk", true, true},
		{"notes/.synk", true, true},
		{"Keep.md", false, false},
	}
	for _, c := range cases {
		if got := m.Match(c.path, c.dir); got != c.match {
			t.Fatalf("%s dir=%v: got %v want %v", c.path, c.dir, got, c.match)
		}
	}
}
