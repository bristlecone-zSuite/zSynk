package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
	"github.com/bristlecone-zSuite/zSynk/internal/identity"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

const base = "http://127.0.0.1:7421"

func TestHealthNeedsNoToken(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodGet, "/v1/health", "")
	req.Header.Del("Authorization")
	if w := serve(s, req); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestOnlyLoopbackClients(t *testing.T) {
	s := newServer(t, "Mac")
	for _, path := range []string{"/v1/health", "/v1/status"} {
		req := request(s, http.MethodGet, path, "")
		req.RemoteAddr = "8.8.8.8:9"
		if w := serve(s, req); w.Code != http.StatusForbidden {
			t.Errorf("%s from 8.8.8.8: %d", path, w.Code)
		}
	}
}

func TestRequestsNeedTheToken(t *testing.T) {
	s := newServer(t, "Mac")
	for _, path := range []string{"/v1/status", "/v1/activity", "/v1/apps", "/v1/libraries", "/v1/libraries/notes/versions", "/v1/pair/code", "/v1/doctor", "/v1/health/../status"} {
		for name, auth := range map[string]string{
			"no token":     "",
			"wrong token":  "Bearer " + strings.Repeat("0", 2*tokenBytes),
			"bare token":   s.token,
			"basic scheme": "Basic " + s.token,
		} {
			req := request(s, http.MethodGet, path, "")
			req.Header.Set("Authorization", auth)
			w := serve(s, req)
			if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
				t.Errorf("%s with %s: %d", path, name, w.Code)
			}
		}
	}
	req := request(s, http.MethodGet, "/v1/status", "")
	req.Header.Set("Authorization", "bearer "+s.token)
	if w := serve(s, req); w.Code != http.StatusOK {
		t.Fatalf("the auth scheme is case-insensitive: %d", w.Code)
	}
}

// A web page can POST to loopback without a CORS preflight if it sends the
// body as text/plain.
func TestCrossOriginPostRejected(t *testing.T) {
	s := newServer(t, "Mac")
	body := jsonBody(t, map[string]string{"id": "home", "preset": "custom", "path": t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, base+"/v1/libraries", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	if w := serve(s, req); w.Code == http.StatusOK || len(s.Node.Libraries()) != 0 {
		t.Fatalf("a web page enabled a sync library with a no-preflight text/plain POST (status %d)", w.Code)
	}

	// Each check holds on its own, even for a client with the token.
	origin := request(s, http.MethodPost, "/v1/libraries", body)
	origin.Header.Set("Origin", "http://127.0.0.1:7421")
	if w := serve(s, origin); w.Code != http.StatusForbidden {
		t.Errorf("Origin: %d", w.Code)
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		req := request(s, http.MethodPost, "/v1/libraries", body)
		req.Header.Set("Content-Type", ct)
		if w := serve(s, req); w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: %d", ct, w.Code)
		}
	}
	if len(s.Node.Libraries()) != 0 {
		t.Fatal("a refused request enabled a library")
	}
	req = request(s, http.MethodPost, "/v1/libraries", body)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if w := serve(s, req); w.Code != http.StatusOK {
		t.Fatalf("JSON with a charset: %d %s", w.Code, w.Body.String())
	}
}

func TestForeignHostRejected(t *testing.T) {
	s := newServer(t, "Mac")
	call(t, s, http.MethodPost, "/v1/pair/start", "", http.StatusOK)

	for _, host := range []string{"rebind.evil.example:7421", "evil.example", "127.0.0.1.evil.example:7421", "192.168.1.5:7421", ""} {
		req := request(s, http.MethodGet, "/v1/pair/code", "")
		req.Host = host
		if w := serve(s, req); w.Code != http.StatusForbidden {
			t.Errorf("a DNS-rebound page on Host %q read the live pairing code and token: %d %s", host, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
	for _, host := range []string{"127.0.0.1:7421", "localhost:7421", "LOCALHOST:7421", "[::1]:7421", "[::1]", "localhost"} {
		req := request(s, http.MethodGet, "/v1/pair/code", "")
		req.Host = host
		if w := serve(s, req); w.Code != http.StatusOK {
			t.Errorf("Host %q: %d", host, w.Code)
		}
	}
}

func TestStatusEncodesEmptyArrays(t *testing.T) {
	s := newServer(t, "Mac")
	var st map[string]json.RawMessage
	decodeBody(t, call(t, s, http.MethodGet, "/v1/status", "", http.StatusOK), &st)
	for _, k := range []string{"libraries", "peers"} {
		if string(st[k]) != "[]" {
			t.Errorf("fresh-install status has %q: %s; SynkClient.swift decodes it as a non-optional array, so the Mac app can't load", k, st[k])
		}
	}
	if got := strings.TrimSpace(call(t, s, http.MethodGet, "/v1/libraries", "", http.StatusOK).Body.String()); got != "[]" {
		t.Errorf("libraries: %s", got)
	}
	var doc map[string]json.RawMessage
	decodeBody(t, call(t, s, http.MethodGet, "/v1/doctor", "", http.StatusOK), &doc)
	if string(doc["issues"]) != "[]" {
		t.Errorf("doctor issues: %s", doc["issues"])
	}
}

// A library with no devices, and a node doing nothing, send arrays too.
func TestLibrariesAndActivityEncodeEmptyArrays(t *testing.T) {
	s := newServer(t, "Mac")
	must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes")}))
	var st struct{ Libraries []map[string]json.RawMessage }
	decodeBody(t, call(t, s, http.MethodGet, "/v1/status", "", http.StatusOK), &st)
	if len(st.Libraries) != 1 || string(st.Libraries[0]["devices"]) != "[]" {
		t.Errorf("a library with no devices: %s", st.Libraries)
	}
	var act map[string]json.RawMessage
	decodeBody(t, call(t, s, http.MethodGet, "/v1/activity", "", http.StatusOK), &act)
	if string(act["tasks"]) != "[]" {
		t.Errorf("activity of an idle node: %s", act["tasks"])
	}
}

// A library's versions are listed newest first, and restoring one keeps
// what it replaces as a version in turn.
func TestVersionsAndRestore(t *testing.T) {
	s := newServer(t, "Mac")
	dir := filepath.Join(t.TempDir(), "notes")
	must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dir}))
	must(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("new"), 0o600))
	keep(t, s, "a.md", "old")

	type versions struct{ Versions []vault.Version }
	var one, all versions
	decodeBody(t, call(t, s, http.MethodGet, "/v1/libraries/notes/versions?path=a.md", "", http.StatusOK), &one)
	decodeBody(t, call(t, s, http.MethodGet, "/v1/libraries/notes/versions", "", http.StatusOK), &all)
	if len(one.Versions) != 1 || one.Versions[0].Path != "a.md" || one.Versions[0].Size != 3 || !slices.Equal(all.Versions, one.Versions) {
		t.Fatalf("a.md's versions %+v; all versions %+v", one.Versions, all.Versions)
	}
	var none map[string]json.RawMessage
	decodeBody(t, call(t, s, http.MethodGet, "/v1/libraries/notes/versions?path=b.md", "", http.StatusOK), &none)
	if string(none["versions"]) != "[]" {
		t.Fatalf("a path with no versions: %s", none["versions"])
	}

	var restored struct{ Restored vault.Version }
	decodeBody(t, call(t, s, http.MethodPost, "/v1/libraries/notes/versions/restore", `{"path":"a.md"}`, http.StatusOK), &restored)
	if restored.Restored != one.Versions[0] {
		t.Fatalf("restored %+v; want %+v", restored.Restored, one.Versions[0])
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(b) != "old" {
		t.Fatalf("a.md holds %q (%v)", b, err)
	}
	decodeBody(t, call(t, s, http.MethodGet, "/v1/libraries/notes/versions?path=a.md", "", http.StatusOK), &one)
	if len(one.Versions) != 2 || one.Versions[0].Size != 3 || one.Versions[0].Reason != "pre-overwrite" {
		t.Fatalf("after the restore, a.md's versions are %+v", one.Versions)
	}
	byID := fmt.Sprintf(`{"path":"a.md","version":%q}`, one.Versions[0].ID)
	call(t, s, http.MethodPost, "/v1/libraries/notes/versions/restore", byID, http.StatusOK)
	if b, err := os.ReadFile(filepath.Join(dir, "a.md")); err != nil || string(b) != "new" {
		t.Fatalf("after restoring a version by ID, a.md holds %q (%v)", b, err)
	}

	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/v1/libraries/nope/versions", "", http.StatusNotFound},
		{http.MethodGet, "/v1/libraries/notes/versions?path=", "", http.StatusBadRequest},
		{http.MethodGet, "/v1/libraries/notes/versions?path=../a.md", "", http.StatusBadRequest},
		{http.MethodPost, "/v1/libraries/notes/versions/restore", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/v1/libraries/notes/versions/restore", `{"path":"a.md","at":"yesterday"}`, http.StatusBadRequest},
		{http.MethodPost, "/v1/libraries/notes/versions/restore", `{"path":"/etc/passwd"}`, http.StatusBadRequest},
		{http.MethodPost, "/v1/libraries/notes/versions/restore", `{"path":"b.md"}`, http.StatusNotFound},
		{http.MethodPost, "/v1/libraries/notes/versions/restore", `{"path":"a.md","version":"nope"}`, http.StatusNotFound},
		{http.MethodPost, "/v1/libraries/nope/versions/restore", `{"path":"a.md"}`, http.StatusNotFound},
	} {
		if w := serve(s, request(s, c.method, c.path, c.body)); w.Code != c.want {
			t.Errorf("%s %s %s: %d %s; want %d", c.method, c.path, c.body, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
	}

	keep(t, s, "c.md", "kept")
	decodeBody(t, call(t, s, http.MethodGet, "/v1/libraries/notes/versions?path=c.md", "", http.StatusOK), &one)
	must(t, os.WriteFile(filepath.Join(s.Node.Vault.PathDir("notes", "c.md"), one.Versions[0].ID, "content"), []byte("rot!"), 0o600))
	call(t, s, http.MethodPost, "/v1/libraries/notes/versions/restore", `{"path":"c.md"}`, http.StatusUnprocessableEntity)
}

// A restore's errors each have their own status, so an app can tell a file
// that changed, which is worth another try, from a damaged copy, which isn't.
func TestRestoreErrorStatus(t *testing.T) {
	for err, want := range map[error]int{
		fmt.Errorf("%w of a.md", vault.ErrNoVersion):                                         http.StatusNotFound,
		fmt.Errorf("%w %q", engine.ErrInvalidPath, "../a.md"):                                http.StatusBadRequest,
		fmt.Errorf("engine: a.md changed as it was restored: %w", apply.ErrChangedSinceScan): http.StatusConflict,
		fmt.Errorf("%w: version 1 of a.md", engine.ErrDamagedVersion):                        http.StatusUnprocessableEntity,
		errors.New("disk full"): http.StatusInternalServerError,
	} {
		w := httptest.NewRecorder()
		writeEngineError(w, err, http.StatusInternalServerError)
		if w.Code != want {
			t.Errorf("%v: %d; want %d", err, w.Code, want)
		}
	}
}

// keep puts content in the vault as the version of rel in notes that a
// sync replaced.
func keep(t *testing.T, s *Server, rel, content string) {
	t.Helper()
	_, err := s.Node.Vault.SnapshotFile("notes", rel, strings.NewReader(content), atomicfile.HashBytes([]byte(content)), "pre-overwrite")
	must(t, err)
}

// Doctor reports what keeps files from syncing.
func TestDiagnose(t *testing.T) {
	lib := config.Library{ID: "notes", Label: "Notes", Path: t.TempDir()}
	for _, c := range []struct {
		state engine.LibraryState
		want  []string
	}{
		{engine.LibraryState{Library: lib, Devices: []engine.DeviceSync{{Name: "iPad", LastSync: time.Now()}}}, []string{}},
		{
			engine.LibraryState{
				Library:      lib,
				ScanErrors:   []string{"a.md: permission denied"},
				Placeholders: []string{".b.md.icloud"},
				Devices:      []engine.DeviceSync{{Name: "iPhone", Failures: 1, Error: "connection refused"}},
			},
			[]string{
				"Notes: can't read a.md: permission denied, so it doesn't sync",
				"Notes: .b.md.icloud isn't downloaded to this device, so it doesn't sync until it is",
				"Notes: the last sync with iPhone failed: connection refused",
			},
		},
		{
			engine.LibraryState{
				Library:      lib,
				ScanErrors:   []string{"a.md: permission denied", "b.md: permission denied"},
				Placeholders: []string{".c.md.icloud", ".d.md.icloud"},
				Devices:      []engine.DeviceSync{{Name: "iPhone", Failures: 3, Error: "connection refused"}, {Name: "iPad"}},
			},
			[]string{
				"Notes: can't read 2 files, so they don't sync. The first is a.md: permission denied",
				"Notes: 2 files aren't downloaded to this device, so they don't sync until they are. The first is .c.md.icloud",
				"Notes: the last 3 syncs with iPhone failed: connection refused",
			},
		},
	} {
		r := Diagnose(engine.Status{Libraries: []engine.LibraryState{c.state}})
		if r.OK != (len(c.want) == 0) || !slices.Equal(r.Issues, c.want) {
			t.Errorf("issues %q (ok %v); want %q", r.Issues, r.OK, c.want)
		}
	}

	same := []engine.LibraryState{
		{Library: config.Library{ID: "notes", Label: "Any folder", Path: lib.Path}, Error: "broken"},
		{Library: config.Library{ID: "photos", Label: "Any folder", Path: lib.Path}, Error: "broken"},
		{Library: config.Library{ID: "obsidian", Label: "Obsidian", Path: lib.Path}, Error: "broken"},
	}
	want := []string{"Any folder (notes): broken", "Any folder (photos): broken", "Obsidian: broken"}
	if got := Diagnose(engine.Status{Libraries: same}).Issues; !slices.Equal(got, want) {
		t.Errorf("libraries with the same label: %q; want %q", got, want)
	}
}

// A paired device that hasn't synced yet has no lastSeen, rather than a
// date in the year 1.
func TestStatusLeavesOutUnknownTimes(t *testing.T) {
	s, a := newServer(t, "Mac"), openNode(t, "iPhone")
	must(t, engine.Pair(s.Node, a))
	peer := func() map[string]json.RawMessage {
		var st struct{ Peers []map[string]json.RawMessage }
		decodeBody(t, call(t, s, http.MethodGet, "/v1/status", "", http.StatusOK), &st)
		if len(st.Peers) != 1 {
			t.Fatalf("peers %v", st.Peers)
		}
		return st.Peers[0]
	}
	if got, ok := peer()["lastSeen"]; ok {
		t.Fatalf("lastSeen %s before the devices synced", got)
	}

	ln, err := a.ServePeers("127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	for _, n := range []*engine.Node{s.Node, a} {
		must(t, n.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: filepath.Join(t.TempDir(), "notes")}))
	}
	must(t, s.Node.SyncWith(ln.Addr().String(), "notes"))
	var seen time.Time
	if err := json.Unmarshal(peer()["lastSeen"], &seen); err != nil || seen.IsZero() {
		t.Fatalf("lastSeen %v after a sync: %v", seen, err)
	}
}

func TestAppEnabledLibrariesShareAnID(t *testing.T) {
	enable := func(s *Server) string {
		w := call(t, s, http.MethodPost, "/v1/libraries", jsonBody(t, map[string]string{"preset": "obsidian", "path": t.TempDir()}), http.StatusOK)
		var out struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		}
		decodeBody(t, w, &out)
		if out.Label != "Obsidian" {
			t.Errorf("label %q", out.Label)
		}
		return out.ID
	}
	studio, laptop := enable(newServer(t, "Studio")), enable(newServer(t, "Laptop"))
	if studio != "obsidian" || laptop != "obsidian" {
		t.Fatalf("Obsidian enabled from the app got library IDs %q and %q on two Macs; sessions match libraries by ID, so both must use the preset's", studio, laptop)
	}
}

func TestMethodsAreEnforced(t *testing.T) {
	s := newServer(t, "Mac")
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/pair/start"},
		{http.MethodGet, "/v1/pair/join"},
		{http.MethodGet, "/v1/sync"},
		{http.MethodPost, "/v1/status"},
		{http.MethodPost, "/v1/pair/code"},
		{http.MethodPut, "/v1/libraries"},
		{http.MethodDelete, "/v1/libraries"},
		{http.MethodGet, "/v1/libraries/notes/deletes/confirm"},
	} {
		if w := serve(s, request(s, c.method, c.path, "")); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
}

func TestBodiesAreStrict(t *testing.T) {
	s := newServer(t, "Mac")
	for name, c := range map[string]struct {
		path, body string
		want       int
	}{
		"too large":         {"/v1/libraries", `{"label":"` + strings.Repeat("x", maxBody) + `"}`, http.StatusRequestEntityTooLarge},
		"an unknown field":  {"/v1/sync", `{"folder":"notes"}`, http.StatusBadRequest},
		"two values":        {"/v1/sync", `{} {}`, http.StatusBadRequest},
		"not JSON":          {"/v1/pair/join", `addr=x`, http.StatusBadRequest},
		"half a sync":       {"/v1/sync", `{"folderId":"notes"}`, http.StatusBadRequest},
		"half a join":       {"/v1/pair/join", `{"addr":"127.0.0.1:1"}`, http.StatusBadRequest},
		"confirm, no paths": {"/v1/libraries/notes/deletes/confirm", `{}`, http.StatusBadRequest},
	} {
		if w := serve(s, request(s, http.MethodPost, c.path, c.body)); w.Code != c.want {
			t.Errorf("%s: %d %s; want %d", name, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
	}
}

func TestEnableLibraryValidates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := newServer(t, "Mac")
	notes := filepath.Join(t.TempDir(), "Notes")
	call(t, s, http.MethodPost, "/v1/libraries", jsonBody(t, map[string]string{"preset": "custom", "id": "notes", "path": notes}), http.StatusOK)

	for name, c := range map[string]struct {
		lib  map[string]string
		want int
	}{
		"an ID that leaves the data folder": {map[string]string{"preset": "custom", "id": "../x", "path": t.TempDir()}, http.StatusBadRequest},
		"an uppercase ID":                   {map[string]string{"preset": "custom", "id": "Notes", "path": t.TempDir()}, http.StatusBadRequest},
		"an unknown preset":                 {map[string]string{"preset": "nope", "path": t.TempDir()}, http.StatusBadRequest},
		"the home folder":                   {map[string]string{"preset": "custom", "id": "home", "path": home}, http.StatusBadRequest},
		"a relative path":                   {map[string]string{"preset": "custom", "id": "rel", "path": "notes"}, http.StatusBadRequest},
		"no folder to default to":           {map[string]string{"preset": "custom", "id": "none"}, http.StatusBadRequest},
		"an ID in use":                      {map[string]string{"preset": "custom", "id": "notes", "path": t.TempDir()}, http.StatusConflict},
	} {
		if w := serve(s, request(s, http.MethodPost, "/v1/libraries", jsonBody(t, c.lib))); w.Code != c.want {
			t.Errorf("%s: %d %s; want %d", name, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
	}
	if libs := s.Node.Libraries(); len(libs) != 1 || libs[0].Path != notes {
		t.Fatalf("libraries after the refusals: %+v", libs)
	}
	call(t, s, http.MethodPost, "/v1/libraries", jsonBody(t, map[string]string{"preset": "obsidian", "id": "notes", "path": notes + string(filepath.Separator)}), http.StatusOK)
	if lib, _ := s.Node.Library("notes"); lib.Preset != "obsidian" || lib.Path != notes {
		t.Fatalf("enabling a library again updates it in place: %+v", lib)
	}
}

func TestHeldDeletesOverAPI(t *testing.T) {
	s := newServer(t, "Mac")
	dir := filepath.Join(t.TempDir(), "notes")
	call(t, s, http.MethodPost, "/v1/libraries", jsonBody(t, map[string]string{"preset": "custom", "id": "notes", "path": dir}), http.StatusOK)
	writeNotes(t, dir, 20)
	scan(t, s)
	removeNotes(t, dir, 15)
	scan(t, s)

	held := heldDeletes(t, s)
	if len(held) != 15 {
		t.Fatalf("held %v", held)
	}
	call(t, s, http.MethodPost, "/v1/libraries/notes/deletes/confirm", jsonBody(t, pathsBody{Paths: held[:3]}), http.StatusConflict)
	var restored pathsBody
	decodeBody(t, call(t, s, http.MethodPost, "/v1/libraries/notes/deletes/restore", "", http.StatusOK), &restored)
	if !sameSet(restored.Paths, held) {
		t.Fatalf("restored %v; held %v", restored.Paths, held)
	}
	for i := range 20 {
		if _, err := os.Stat(filepath.Join(dir, note(i))); err != nil {
			t.Fatalf("not restored: %v", err)
		}
	}
	if got := heldDeletes(t, s); len(got) != 0 {
		t.Fatalf("still held after the restore: %v", got)
	}

	removeNotes(t, dir, 15)
	scan(t, s)
	held = heldDeletes(t, s)
	var confirmed pathsBody
	decodeBody(t, call(t, s, http.MethodPost, "/v1/libraries/notes/deletes/confirm", jsonBody(t, pathsBody{Paths: held}), http.StatusOK), &confirmed)
	if len(held) != 15 || !sameSet(confirmed.Paths, held) {
		t.Fatalf("confirmed %v; held %v", confirmed.Paths, held)
	}
	if e := s.Node.Index("notes").Entries[note(0)]; !e.Deleted {
		t.Fatalf("a confirmed delete isn't a tombstone: %+v", e)
	}
	for path, body := range map[string]string{"/v1/libraries/notes/deletes/confirm": `{"paths":[]}`, "/v1/libraries/notes/deletes/restore": ""} {
		var raw map[string]json.RawMessage
		decodeBody(t, call(t, s, http.MethodPost, path, body, http.StatusOK), &raw)
		if string(raw["paths"]) != "[]" {
			t.Errorf("%s with nothing held: %s", path, raw["paths"])
		}
	}
	call(t, s, http.MethodPost, "/v1/libraries/notes/deletes/restore", `{"paths":["n00.md"]}`, http.StatusBadRequest)
	call(t, s, http.MethodPost, "/v1/libraries/nope/deletes/restore", "", http.StatusNotFound)
	call(t, s, http.MethodPost, "/v1/libraries/nope/deletes/confirm", `{"paths":[]}`, http.StatusNotFound)
}

func TestTokenIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	tok, err := LoadOrCreateToken(dir)
	must(t, err)
	again, err := LoadOrCreateToken(dir)
	must(t, err)
	if tok != again || !validToken(tok) {
		t.Fatalf("%q, then %q", tok, again)
	}
	if read, err := ReadToken(dir); err != nil || read != tok {
		t.Fatalf("ReadToken: %q, %v", read, err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(TokenPath(dir))
		must(t, err)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode %v", st.Mode().Perm())
		}
		must(t, os.Chmod(TokenPath(dir), 0o644))
		fresh, err := LoadOrCreateToken(dir)
		must(t, err)
		if fresh == tok {
			t.Fatal("kept a token that other users could read")
		}
		tok = fresh
	}
	must(t, os.WriteFile(TokenPath(dir), []byte("short\n"), 0o600))
	if fresh, err := LoadOrCreateToken(dir); err != nil || fresh == tok || !validToken(fresh) {
		t.Fatalf("after a malformed token file: %q, %v", fresh, err)
	}
}

func TestServeOnlyOnLoopback(t *testing.T) {
	s := newServer(t, "Mac")
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:0", "[::]:0"} {
		if err := s.Listen(addr); err == nil {
			t.Errorf("%s: no error", addr)
		}
	}
	if s.Addr() != "" {
		t.Fatalf("listening on %s", s.Addr())
	}
}

func TestServeUntilShutdown(t *testing.T) {
	s := newServer(t, "Mac")
	if err := s.Serve(); err == nil {
		t.Fatal("served without a socket")
	}
	must(t, s.Listen("127.0.0.1:0"))
	if err := s.Listen("127.0.0.1:0"); err == nil {
		t.Fatal("listened twice")
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	c, err := Connect(s.Node.DataDir, s.Addr())
	must(t, err)
	st, err := c.Status()
	must(t, err)
	if st.DeviceID != s.Node.Device.DeviceID {
		t.Fatalf("status %+v", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, s.Shutdown(ctx))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve kept running after Shutdown")
	}
	if _, err := c.Status(); err == nil {
		t.Fatal("the API still answers after Shutdown")
	}
}

func TestRealClients(t *testing.T) {
	s := newServer(t, "Mac")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(header http.Header) int {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/status", nil)
		must(t, err)
		maps.Copy(req.Header, header)
		resp, err := srv.Client().Do(req)
		must(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	bearer := []string{"Bearer " + s.token}
	if code := get(http.Header{"Authorization": bearer}); code != http.StatusOK {
		t.Errorf("with the token: %d", code)
	}
	if code := get(http.Header{"Authorization": bearer, "Origin": {"https://evil.example"}}); code != http.StatusForbidden {
		t.Errorf("from a web page: %d", code)
	}
	if code := get(nil); code != http.StatusUnauthorized {
		t.Errorf("without the token: %d", code)
	}
}

// The offer, and the link in its QR code, carry what the joining device
// needs: where to connect, the code and token, and the key to expect.
func TestPairStartArmsEngine(t *testing.T) {
	s := newServer(t, "Mac")
	b := openNode(t, "Phone")
	ln, err := s.Node.ServePeers("127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	call(t, s, http.MethodGet, "/v1/pair/code", "", http.StatusNotFound)
	var offer Offer
	decodeBody(t, call(t, s, http.MethodPost, "/v1/pair/start", "", http.StatusOK), &offer)
	pub, err := s.Node.Device.Public()
	must(t, err)
	if offer.Code == "" || offer.Token == "" || offer.DeviceID != s.Node.Device.DeviceID || offer.Name != "Mac" ||
		offer.Fingerprint != identity.Fingerprint(pub) || !slices.Equal(offer.Addresses, []string{ln.Addr().String()}) ||
		time.Until(offer.ExpiresAt) < 59*time.Minute {
		t.Fatalf("offer %+v", offer)
	}
	link, err := pairing.ParseURI(offer.QR)
	must(t, err)
	if link.Code != offer.Code || link.Token != offer.Token || link.Fingerprint != offer.Fingerprint || !slices.Equal(link.Addrs, offer.Addresses) {
		t.Fatalf("the QR link %s doesn't match the offer %+v", offer.QR, offer)
	}
	var waiting Offer
	decodeBody(t, call(t, s, http.MethodGet, "/v1/pair/code", "", http.StatusOK), &waiting)
	if waiting.QR != offer.QR || !waiting.ExpiresAt.Equal(offer.ExpiresAt) {
		t.Fatalf("the waiting offer %+v isn't the one made: %+v", waiting, offer)
	}
	p, err := pairing.LoadPending(s.Node.DataDir)
	if err != nil || p == nil {
		t.Fatalf("pairing must be on disk: %v", err)
	}
	must(t, b.JoinPeer(link.Addrs[0], link.Code, link.Token, link.Fingerprint))
	call(t, s, http.MethodGet, "/v1/pair/code", "", http.StatusNotFound)
}

func TestPairJoinAPI(t *testing.T) {
	a := openNode(t, "Mac")
	ln, err := a.ServePeers("127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	addr := ln.Addr().String()
	sess, err := pairing.NewSession()
	must(t, err)
	must(t, a.ArmPairing(sess.Code, sess.Token))
	s := newServer(t, "Phone")
	other, err := pairing.NewSession()
	must(t, err)
	for name, c := range map[string]struct {
		body joinBody
		want int
	}{
		"a wrong token":           {joinBody{Addr: addr, Code: sess.Code, Token: other.Token}, http.StatusBadRequest},
		"a malformed token":       {joinBody{Addr: addr, Code: sess.Code, Token: "x"}, http.StatusBadRequest},
		"another fingerprint":     {joinBody{Addr: addr, Code: sess.Code, Token: sess.Token, Fingerprint: s.Node.Fingerprint()}, http.StatusBadRequest},
		"a malformed fingerprint": {joinBody{Addr: addr, Code: sess.Code, Token: sess.Token, Fingerprint: "abc"}, http.StatusBadRequest},
		"nothing at the address":  {joinBody{Addr: "127.0.0.1:1", Code: sess.Code, Token: sess.Token}, http.StatusBadGateway},
	} {
		if w := serve(s, request(s, http.MethodPost, "/v1/pair/join", jsonBody(t, c.body))); w.Code != c.want {
			t.Errorf("%s: %d %s; want %d", name, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
	}
	if len(s.Node.Status().Peers) != 0 {
		t.Fatal("a failed join paired")
	}
	call(t, s, http.MethodPost, "/v1/pair/join", jsonBody(t, joinBody{Addr: addr, Code: sess.Code, Token: sess.Token, Fingerprint: a.Fingerprint()}), http.StatusOK)
	if _, ok := s.Node.Peer(a.Device.DeviceID); !ok {
		t.Fatal("join via API did not pair")
	}
}

// Syncing with a device that isn't paired, or a library it isn't shared
// with, is a conflict to fix here, not a network failure.
func TestSyncAPIStatusCodes(t *testing.T) {
	a, s := openNode(t, "Mac"), newServer(t, "Phone")
	ln, err := a.ServePeers("127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	addr := ln.Addr().String()
	dirA := filepath.Join(t.TempDir(), "notes")
	must(t, a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dirA}))
	dir := filepath.Join(t.TempDir(), "notes")
	share := func(ids ...string) {
		must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dir, ShareWith: ids}))
	}
	sync := func(want int) {
		t.Helper()
		call(t, s, http.MethodPost, "/v1/sync", jsonBody(t, syncBody{Addr: addr, FolderID: "notes"}), want)
	}
	share()
	sync(http.StatusConflict)
	must(t, engine.Pair(a, s.Node))
	third := openNode(t, "iPad")
	must(t, engine.Pair(s.Node, third))
	share(third.Device.DeviceID)
	sync(http.StatusConflict)
	share([]string{}...)
	sync(http.StatusOK)
	must(t, s.Node.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dir, Paused: true}))
	sync(http.StatusConflict)
	share([]string{}...)
	must(t, engine.Pair(a, third))
	must(t, a.EnableLibrary(config.Library{ID: "notes", Preset: "custom", Path: dirA, ShareWith: []string{third.Device.DeviceID}}))
	sync(http.StatusConflict)
	call(t, s, http.MethodPost, "/v1/sync", jsonBody(t, syncBody{Addr: "127.0.0.1:1", FolderID: "notes"}), http.StatusBadGateway)
}

func openNode(t *testing.T, name string) *engine.Node {
	t.Helper()
	return openNodeAt(t, t.TempDir(), name)
}

func openNodeAt(t *testing.T, dir, name string) *engine.Node {
	t.Helper()
	n, err := engine.Open(dir, name)
	must(t, err)
	n.PackageSettle = 0
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func newServer(t *testing.T, name string) *Server {
	t.Helper()
	s, err := New(openNode(t, name))
	must(t, err)
	return s
}

// request builds a request the way Synk's own clients send one.
func request(s *Server, method, path, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, base+path, rd)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer "+s.token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func call(t *testing.T, s *Server, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := serve(s, request(s, method, path, body))
	if w.Code != want {
		t.Fatalf("%s %s: %d %s; want %d", method, path, w.Code, strings.TrimSpace(w.Body.String()), want)
	}
	return w
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return string(b)
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
}

func scan(t *testing.T, s *Server) {
	t.Helper()
	_, err := s.Node.ScanLibrary("notes")
	must(t, err)
}

func heldDeletes(t *testing.T, s *Server) []string {
	t.Helper()
	var st engine.Status
	decodeBody(t, call(t, s, http.MethodGet, "/v1/status", "", http.StatusOK), &st)
	if len(st.Libraries) != 1 {
		t.Fatalf("libraries %+v", st.Libraries)
	}
	return st.Libraries[0].HeldDeletes
}

func note(i int) string { return fmt.Sprintf("n%02d.md", i) }

func writeNotes(t *testing.T, dir string, count int) {
	t.Helper()
	for i := range count {
		must(t, os.WriteFile(filepath.Join(dir, note(i)), []byte(fmt.Sprintf("note %d", i)), 0o600))
	}
}

func removeNotes(t *testing.T, dir string, count int) {
	t.Helper()
	for i := range count {
		must(t, os.Remove(filepath.Join(dir, note(i))))
	}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfigEncryption(t *testing.T) {
	s := newServer(t, "Mac")
	if s.Node.Status().VaultEncrypted {
		t.Fatal("a fresh node starts plain")
	}
	post := func(path, body string) int {
		return serve(s, request(s, http.MethodPost, path, body)).Code
	}
	if code := post("/v1/config/encryption/enable", `{"passphrase":""}`); code != http.StatusBadRequest {
		t.Errorf("empty passphrase: got %d, want 400", code)
	}
	if code := post("/v1/config/encryption/enable", `{"passphrase":"correct horse"}`); code != http.StatusOK {
		t.Errorf("enable: got %d, want 200", code)
	}
	if !s.Node.Status().VaultEncrypted {
		t.Error("enable must set vaultEncrypted")
	}
	if code := post("/v1/config/encryption/enable", `{"passphrase":"correct horse"}`); code != http.StatusBadRequest {
		t.Errorf("enable twice: got %d, want 400", code)
	}
	if code := post("/v1/config/encryption/disable", `{"passphrase":"wrong horse"}`); code != http.StatusForbidden {
		t.Errorf("disable with the wrong passphrase: got %d, want 403", code)
	}
	if code := post("/v1/config/encryption/disable", `{"passphrase":"correct horse"}`); code != http.StatusOK {
		t.Errorf("disable: got %d, want 200", code)
	}
	if s.Node.Status().VaultEncrypted {
		t.Error("disable must clear vaultEncrypted")
	}
}
