package ignore

import (
	"path"
	"strings"
)

// Matcher is a small gitignore-style matcher (enough for app presets).
// Patterns are matched against slash-separated relative paths.
type Matcher struct {
	patterns []pat
}

type pat struct {
	raw      string
	dirOnly  bool
	neg      bool
	segments []string
}

func Compile(patterns []string) *Matcher {
	m := &Matcher{}
	for _, raw := range patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		p := pat{raw: raw}
		if strings.HasPrefix(raw, "!") {
			p.neg = true
			raw = raw[1:]
		}
		raw = strings.ReplaceAll(raw, "\\", "/")
		if strings.HasSuffix(raw, "/") {
			p.dirOnly = true
			raw = strings.TrimSuffix(raw, "/")
		}
		p.segments = strings.Split(raw, "/")
		m.patterns = append(m.patterns, p)
	}
	return m
}

func (m *Matcher) Match(rel string, isDir bool) bool {
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimPrefix(rel, "./")
	matched := false
	for _, p := range m.patterns {
		ok := false
		if p.dirOnly {
			ok = matchDir(p.segments, rel, isDir)
		} else {
			ok = matchPath(p.segments, rel)
		}
		if !ok {
			continue
		}
		if p.neg {
			matched = false
		} else {
			matched = true
		}
	}
	return matched
}

// matchDir matches a pattern that ends in "/": the folder itself, when rel
// is a folder, or anything inside it.
func matchDir(pattern []string, rel string, isDir bool) bool {
	parts := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	last := len(parts) - 1
	if len(pattern) == 1 && pattern[0] != "" {
		// "node_modules/" matches a folder with that name at any depth.
		for i, part := range parts {
			if (i < last || isDir) && matchGlob(pattern[0], part) {
				return true
			}
		}
		return false
	}
	pat := pattern
	if len(pat) > 0 && pat[0] == "" {
		pat = pat[1:]
	}
	if len(parts) < len(pat) || len(parts) == len(pat) && !isDir {
		return false
	}
	for i := range pat {
		if !matchGlob(pat[i], parts[i]) {
			return false
		}
	}
	return true
}

func matchPath(pattern []string, rel string) bool {
	if len(pattern) == 1 && pattern[0] != "" {
		return matchAnywhere(pattern[0], rel)
	}
	name := strings.TrimPrefix(rel, "/")
	parts := strings.Split(name, "/")
	if pattern[0] == "" {
		return matchExact(pattern[1:], parts)
	}
	return matchAnywhereSegments(pattern, parts)
}

func matchAnywhere(glob, rel string) bool {
	if matchGlob(glob, path.Base(rel)) {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if matchGlob(glob, part) {
			return true
		}
	}
	ok, err := path.Match(glob, rel)
	return err == nil && ok
}

func matchAnywhereSegments(pattern, parts []string) bool {
	for i := 0; i <= len(parts); i++ {
		if matchExact(pattern, parts[i:]) {
			return true
		}
	}
	return false
}

func matchExact(pattern, parts []string) bool {
	i, j := 0, 0
	for i < len(pattern) {
		if pattern[i] == "**" {
			if i == len(pattern)-1 {
				return true
			}
			for j <= len(parts) {
				if matchExact(pattern[i+1:], parts[j:]) {
					return true
				}
				j++
			}
			return false
		}
		if j >= len(parts) {
			return false
		}
		if !matchGlob(pattern[i], parts[j]) {
			return false
		}
		i++
		j++
	}
	return j == len(parts)
}

func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// GlobalPatterns are always excluded from libraries.
func GlobalPatterns() []string {
	return []string{
		".DS_Store",
		".AppleDouble",
		".LSOverride",
		"._*",
		".Spotlight-V100",
		".Trashes",
		".fseventsd",
		".TemporaryItems",
		"Thumbs.db",
		"*.icloud",
		".synk/",
		"*.synk-tmp-*",
	}
}
