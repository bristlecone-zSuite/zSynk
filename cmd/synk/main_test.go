package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/api"
	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

func TestShareList(t *testing.T) {
	for in, want := range map[string][]string{
		"":                     nil,
		"all":                  {},
		"AAAA-BBBB-CCCC-DDDD":  {"AAAA-BBBB-CCCC-DDDD"},
		"AAAA-BBBB, CCCC-DDDD": {"AAAA-BBBB", "CCCC-DDDD"},
		"a,b c,,":              {"a", "b", "c"},
	} {
		got, err := shareList(in)
		if err != nil || !slices.Equal(got, want) || (got == nil) != (want == nil) {
			t.Errorf("shareList(%q) = %#v, %v; want %#v", in, got, err, want)
		}
	}
	for _, in := range []string{",", " , "} {
		if got, err := shareList(in); err == nil {
			t.Errorf("shareList(%q) = %#v; an empty list would share with every device", in, got)
		}
	}
}

func TestSyncState(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for d, want := range map[engine.DeviceSync]string{
		{Name: "iPhone"}: "iPhone: hasn't synced since synk serve started",
		{Name: "iPhone", LastSync: now.Add(-90 * time.Second)}:                                          "iPhone: synced 1m30s ago",
		{Name: "iPhone", LastSync: now.Add(-time.Hour), Failures: 2, RetryAt: now.Add(7 * time.Second)}: "iPhone: synced 1h0m0s ago; failing (2 in a row), next try in 7s",
		{Name: "iPhone", Failures: 1, RetryAt: now.Add(-time.Second)}:                                   "iPhone: hasn't synced since synk serve started; failing (1 in a row), next try soon",
	} {
		if got := syncState(d, now); got != want {
			t.Errorf("syncState(%+v) = %q; want %q", d, got, want)
		}
	}
}

// pair tries a link's other addresses only when one didn't answer.
func TestRejected(t *testing.T) {
	for err, want := range map[error]bool{
		&api.Error{Status: http.StatusBadGateway, Message: "dial"}:   false,
		&api.Error{Status: http.StatusBadRequest, Message: "token"}:  true,
		&api.Error{Status: http.StatusConflict, Message: "conflict"}: true,
		fmt.Errorf("%w: wrong token", engine.ErrPairingRejected):     true,
		errors.New("dial tcp 192.168.1.5:7420: connection refused"):  false,
	} {
		if got := rejected(err); got != want {
			t.Errorf("rejected(%v) = %v", err, got)
		}
	}
}

// Paths name files in a library whether they're relative to it or
// absolute, through the daemon or not, and even through a symlink.
func TestInLibrary(t *testing.T) {
	n, err := engine.Open(t.TempDir(), "Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("can't make a symlink here:", err)
	}
	work := t.TempDir()
	workLink := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(work, workLink); err != nil {
		t.Fatal(err)
	}
	for _, l := range []config.Library{{ID: "notes", Path: link}, {ID: "work", Path: work}} {
		l.Preset = "custom"
		if err := n.EnableLibrary(l); err != nil {
			t.Fatal(err)
		}
	}
	s, err := api.New(n)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c, err := api.Connect(n.DataDir, strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tg := range map[string]*target{"node": {node: n}, "daemon": {daemon: c}} {
		for path, want := range map[string]string{
			"":                                   "",
			"a b.md":                             "a b.md",
			filepath.Join("notes", "a.md"):       "notes/a.md",
			"./notes/../a.md":                    "a.md",
			link:                                 "",
			filepath.Join(link, "notes", "a.md"): "notes/a.md",
			filepath.Join(real, "notes", "a.md"): "notes/a.md",
		} {
			if got, err := tg.inLibrary("notes", path); err != nil || got != want {
				t.Errorf("%s: inLibrary(%q) = %q, %v; want %q", name, path, got, err, want)
			}
		}
		for _, path := range []string{filepath.Dir(real), filepath.Join(real, "..", "b.md"), t.TempDir()} {
			if got, err := tg.inLibrary("notes", path); err == nil {
				t.Errorf("%s: inLibrary(%q) = %q; it isn't in the library", name, path, got)
			}
		}
		if got, err := tg.inLibrary("work", filepath.Join(workLink, "x.md")); err != nil || got != "x.md" {
			t.Errorf("%s: a path through a symlink to the library = %q, %v", name, got, err)
		}
		if _, err := tg.inLibrary("photos", real); !errors.Is(err, engine.ErrUnknownLibrary) {
			t.Errorf("%s: an unknown library: %v", name, err)
		}
	}
}

// Without a daemon, synk versions and synk restore open the data folder
// themselves, and what restore does goes in synk.log.
func TestRestoreCommand(t *testing.T) {
	data := t.TempDir()
	t.Setenv("SYNK_DIR", data)
	dir := filepath.Join(t.TempDir(), "notes")
	tg, err := connect("Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := tg.node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.node.Vault.SnapshotFile("notes", "a.md", strings.NewReader("old"), atomicfile.HashBytes([]byte("old")), "pre-overwrite"); err != nil {
		t.Fatal(err)
	}
	tg.close()

	out, err := output(t, func() error { return cmdVersions([]string{"--library", "notes"}) })
	if err != nil || !regexp.MustCompile(`(?m)^KEPT +BEFORE IT WAS +SIZE +PATH +VERSION\n.+ replaced +3 B +a\.md +\d{8}T`).MatchString(out) {
		t.Fatalf("synk versions printed (%v):\n%s", err, out)
	}
	out, err = output(t, func() error {
		return cmdRestore([]string{"--library", "notes", "--path", filepath.Join(dir, "a.md")})
	})
	if err != nil || !strings.HasPrefix(out, "Restored a.md as it was before it was replaced, at ") || !strings.HasSuffix(out, "Paired devices get it once synk serve runs.\n") {
		t.Fatalf("synk restore printed (%v):\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(b) != "old" {
		t.Fatalf("a.md holds %q (%v)", b, err)
	}
	if log, err := os.ReadFile(logPath(data)); err != nil || !strings.Contains(string(log), `level=INFO msg="restored a version" library=notes path=a.md`) {
		t.Fatalf("synk.log (%v):\n%s", err, log)
	}
	if _, err := output(t, func() error { return cmdRestore([]string{"--library", "notes", "--path", dir}) }); err == nil || !strings.Contains(err.Error(), "is the library's folder") {
		t.Fatalf("restoring the library's folder: %v", err)
	}
	if out, err := output(t, func() error { return cmdVersions([]string{"--library", "notes", "--path", "b.md"}) }); err != nil || out != "No versions are kept.\n" {
		t.Fatalf("synk versions of a file with none printed (%v):\n%s", err, out)
	}
}

// TestIgnoreCommand covers `synk ignore`: listing a library's patterns,
// adding, removing exact matches, clearing, and the error cases.
func TestIgnoreCommand(t *testing.T) {
	data := t.TempDir()
	t.Setenv("SYNK_DIR", data)
	dir := filepath.Join(t.TempDir(), "notes")
	tg, err := connect("Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	if err := tg.node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dir}); err != nil {
		t.Fatal(err)
	}
	tg.close()

	out, err := output(t, func() error { return cmdIgnore([]string{"--library", "notes"}) })
	if err != nil || out != "No ignore patterns for notes.\n" {
		t.Fatalf("empty list (%v):\n%s", err, out)
	}

	out, err = output(t, func() error {
		return cmdIgnore([]string{"--library", "notes", "--add", " Archive/** ", "--add", "Temp/*"})
	})
	if err != nil || out != "Ignore patterns for notes:\n  Archive/**\n  Temp/*\n" {
		t.Fatalf("add (%v):\n%s", err, out)
	}
	tg, err = connect("Studio Mac")
	if err != nil {
		t.Fatal(err)
	}
	lib, ok := tg.node.Library("notes")
	if !ok || len(lib.Ignore) != 2 || lib.Ignore[0] != "Archive/**" || lib.Ignore[1] != "Temp/*" {
		t.Fatalf("patterns not saved: %+v (%v)", lib.Ignore, ok)
	}
	tg.close()

	out, err = output(t, func() error {
		return cmdIgnore([]string{"--library", "notes", "--remove", "Temp/*"})
	})
	if err != nil || out != "Ignore patterns for notes:\n  Archive/**\n" {
		t.Fatalf("remove (%v):\n%s", err, out)
	}
	out, err = output(t, func() error { return cmdIgnore([]string{"--library", "notes", "--clear"}) })
	if err != nil || out != "No ignore patterns for notes.\n" {
		t.Fatalf("clear (%v):\n%s", err, out)
	}
	if _, err := output(t, func() error { return cmdIgnore([]string{"--library", "other"}) }); err == nil {
		t.Fatal("ignore of an unknown library succeeded")
	}
	if _, err := output(t, func() error { return cmdIgnore([]string{}) }); err == nil || !strings.Contains(err.Error(), "--library is required") {
		t.Fatalf("ignore without --library: %v", err)
	}
	if _, err := output(t, func() error { return cmdIgnore([]string{"--library", "notes", "--add", "--clear"}) }); err == nil {
		t.Fatal("--add together with --clear succeeded")
	}
}

// TestRelayCommand covers `synk relay`: setting a ws or wss URL, printing
// the current one, clearing it, and rejecting everything else.
func TestRelayCommand(t *testing.T) {
	t.Setenv("SYNK_DIR", t.TempDir())

	out, err := output(t, func() error { return cmdRelay([]string{}) })
	if err != nil || out != "No relay is set. Direct LAN connections only.\n" {
		t.Fatalf("synk relay with none set (%v):\n%s", err, out)
	}

	out, err = output(t, func() error { return cmdRelay([]string{"wss://relay.example.com"}) })
	if err != nil || out != "Relay set: wss://relay.example.com\n" {
		t.Fatalf("synk relay set (%v):\n%s", err, out)
	}
	n, err := openNode("")
	if err != nil {
		t.Fatal(err)
	}
	if n.Config.Relay != "wss://relay.example.com" {
		n.Close()
		t.Fatalf("config not updated: %q", n.Config.Relay)
	}
	n.Close()

	out, err = output(t, func() error { return cmdRelay([]string{}) })
	if err != nil || out != "Relay: wss://relay.example.com\n" {
		t.Fatalf("synk relay with one set (%v):\n%s", err, out)
	}

	out, err = output(t, func() error { return cmdRelay([]string{"clear"}) })
	if err != nil || out != "Relay cleared. Direct LAN connections only.\n" {
		t.Fatalf("synk relay clear (%v):\n%s", err, out)
	}
	if out, err = output(t, func() error { return cmdRelay([]string{}) }); err != nil || out != "No relay is set. Direct LAN connections only.\n" {
		t.Fatalf("synk relay after clear (%v):\n%s", err, out)
	}

	for _, bad := range []string{"ftp://relay.example.com", "wss://", "https://relay.example.com"} {
		if _, err := output(t, func() error { return cmdRelay([]string{bad}) }); err == nil {
			t.Fatalf("synk relay %q: accepted", bad)
		}
	}
	if n, err := openNode(""); err == nil {
		defer n.Close()
		if n.Config.Relay != "" {
			t.Fatalf("rejected URL changed config: %q", n.Config.Relay)
		}
	}
}

// output returns what f prints.
func output(t *testing.T, f func() error) (string, error) {
	t.Helper()
	tmp, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	stdout := os.Stdout
	os.Stdout = tmp
	err = f()
	os.Stdout = stdout
	b, rerr := os.ReadFile(tmp.Name())
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(b), err
}

func TestWhy(t *testing.T) {
	for reason, want := range map[string]string{"pre-overwrite": "replaced", "pre-delete": "deleted", "moved": "moved"} {
		if got := why(reason); got != want {
			t.Errorf("why(%q) = %q; want %q", reason, got, want)
		}
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{
		0:                 "0 B",
		999:               "999 B",
		1000:              "1.0 KB",
		1949:              "1.9 KB",
		999_949:           "999.9 KB",
		999_950:           "1.0 MB",
		10 << 30:          "10.7 GB",
		1_500_000_000_000: "1.5 TB",
		math.MaxInt64:     "9.2 EB",
	} {
		if got := size(n); got != want {
			t.Errorf("size(%d) = %q; want %q", n, got, want)
		}
	}
}

func TestEncryptCommand(t *testing.T) {
	t.Setenv("SYNK_DIR", t.TempDir())

	// No passphrase and no terminal: refused.
	if _, err := output(t, func() error { return cmdEncrypt([]string{}) }); err == nil {
		t.Fatal("synk encrypt without a passphrase must fail")
	}

	out, err := output(t, func() error { return cmdEncrypt([]string{"--passphrase", "correct horse"}) })
	if err != nil || !strings.Contains(out, "encrypted") {
		t.Fatalf("synk encrypt (%v):\n%s", err, out)
	}
	root := filepath.Join(os.Getenv("SYNK_DIR"), "vault")
	if !vault.IsEncrypted(root) {
		t.Fatal("the vault is not encrypted on disk")
	}

	// With the vault encrypted, a second enable through a locked node
	// must fail.
	t.Setenv("SYNK_VAULT_PASSPHRASE", "correct horse")
	if _, err := output(t, func() error { return cmdEncrypt([]string{"--passphrase", "correct horse"}) }); err == nil {
		t.Fatal("enabling twice must fail")
	}

	// Turning it off needs the right passphrase.
	if _, err := output(t, func() error { return cmdEncrypt([]string{"--off", "--passphrase", "wrong horse"}) }); err == nil {
		t.Fatal("disable with the wrong passphrase must fail")
	}
	out, err = output(t, func() error { return cmdEncrypt([]string{"--off", "--passphrase", "correct horse"}) })
	if err != nil || !strings.Contains(out, "plain") {
		t.Fatalf("synk encrypt --off (%v):\n%s", err, out)
	}
	if vault.IsEncrypted(root) {
		t.Fatal("the vault is still encrypted after --off")
	}
}
