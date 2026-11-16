package safepath

import "testing"

func TestCheck(t *testing.T) {
	ok := []string{"a.md", "notes/todo.md", ".obsidian/app.json", "Talk.key/Data/img.png", "..md", "a..b", "x/.synkrc"}
	bad := []string{
		"", ".", "..", "../x", "a/../../x", "/etc/passwd", "a//b", "a/./b", "a/", "./a",
		"a\\b", "a\x00b", "bad\xffname", ".synk", ".synk/staging/x", ".SYNK/x", "sub/.synk/x", "sub/.Synk",
	}
	for _, p := range ok {
		if err := Check(p); err != nil {
			t.Errorf("Check(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range bad {
		if err := Check(p); err == nil {
			t.Errorf("Check(%q) = nil, want error", p)
		}
	}
}

func FuzzCheck(f *testing.F) {
	for _, s := range []string{"a.md", "../x", "a/b", ".synk/x", "a\\b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		if Check(rel) != nil {
			return
		}
		for i := 0; i+1 < len(rel); i++ {
			if rel[i] == '.' && rel[i+1] == '.' && (i == 0 || rel[i-1] == '/') && (i+2 == len(rel) || rel[i+2] == '/') {
				t.Fatalf("accepted %q with a .. component", rel)
			}
		}
		if rel[0] == '/' {
			t.Fatalf("accepted absolute %q", rel)
		}
	})
}
