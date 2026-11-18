package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/apply"
	"github.com/bristlecone-zSuite/zSynk/internal/apps"
	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
	"github.com/bristlecone-zSuite/zSynk/internal/pairing"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
	"github.com/bristlecone-zSuite/zSynk/relay"
)

// maxBody bounds request bodies; the largest is a list of held deletes.
const maxBody = 1 << 20

type Server struct {
	Node     *engine.Node
	token    string
	mu       sync.Mutex
	listener net.Listener
	srv      *http.Server
}

// New returns the local API for n. Every request except /v1/health needs
// the token in n's data folder, which New creates if there isn't one.
func New(n *engine.Node) (*Server, error) {
	tok, err := LoadOrCreateToken(n.DataDir)
	if err != nil {
		return nil, fmt.Errorf("api: token: %w", err)
	}
	return &Server{Node: n, token: tok}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/activity", s.activity)
	mux.HandleFunc("GET /v1/apps", s.apps)
	mux.HandleFunc("GET /v1/libraries", s.listLibraries)
	mux.HandleFunc("POST /v1/libraries", s.enableLibrary)
	mux.HandleFunc("POST /v1/libraries/{id}/deletes/confirm", s.confirmDeletes)
	mux.HandleFunc("POST /v1/libraries/{id}/deletes/restore", s.restoreDeletes)
	mux.HandleFunc("GET /v1/libraries/{id}/versions", s.versions)
	mux.HandleFunc("POST /v1/libraries/{id}/versions/restore", s.restoreVersion)
	mux.HandleFunc("POST /v1/libraries/{id}/pause", s.pauseLibrary)
	mux.HandleFunc("POST /v1/libraries/{id}/resume", s.resumeLibrary)
	mux.HandleFunc("DELETE /v1/libraries/{id}", s.removeLibrary)
	mux.HandleFunc("DELETE /v1/peers/{id}", s.unpair)
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("POST /v1/pair/start", s.pairStart)
	mux.HandleFunc("POST /v1/pair/join", s.pairJoin)
	mux.HandleFunc("GET /v1/pair/code", s.pairCode)
	mux.HandleFunc("POST /v1/sync", s.sync)
	mux.HandleFunc("GET /v1/doctor", s.doctor)
	mux.HandleFunc("POST /v1/config/name", s.setDeviceName)
	mux.HandleFunc("POST /v1/config/relay", s.setRelay)
	mux.HandleFunc("GET /v1/login", s.getLogin)
	mux.HandleFunc("POST /v1/login", s.enableLogin)
	mux.HandleFunc("DELETE /v1/login", s.disableLogin)
	return guard(s.token, mux)
}

// Listen opens the API's socket on addr, which must be a loopback address.
func (s *Server) Listen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if !loopbackName(host) {
		return fmt.Errorf("api: not listening on %s; the local API only serves this device", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		_ = ln.Close()
		return errors.New("api: already listening")
	}
	s.listener, s.srv = ln, srv
	return nil
}

// Serve serves the API on the socket Listen opened until Shutdown.
func (s *Server) Serve() error {
	s.mu.Lock()
	ln, srv := s.listener, s.srv
	s.mu.Unlock()
	if srv == nil {
		return errors.New("api: Serve before Listen")
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops Serve, letting requests in progress finish until ctx ends.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": "synk"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Node.Status())
}

// activity is what the node is doing now. Unlike status, which waits for a
// scan or an apply in progress to finish, it answers at once.
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.Node.Activity()})
}

func (s *Server) apps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"catalog":    apps.Catalog(),
		"discovered": apps.Discover(),
	})
}

func (s *Server) listLibraries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Node.Libraries())
}

// enableLibrary adds a library. The ID defaults to the preset's, so the same
// app enabled on two devices syncs between them.
func (s *Server) enableLibrary(w http.ResponseWriter, r *http.Request) {
	var l config.Library
	if !decode(w, r, &l) {
		return
	}
	if l.ID == "" {
		l.ID = l.Preset
	}
	if l.Path == "" {
		p, err := apps.DefaultPath(l.Preset)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		l.Path = p
	}
	if err := s.Node.EnableLibrary(l); err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	lib, _ := s.Node.Library(l.ID)
	writeJSON(w, http.StatusOK, lib)
}

type pathsBody struct {
	Paths []string `json:"paths"`
}

// confirmDeletes sends a library's held deletes to peers. The body lists
// the deletes the user was shown, and they must still be the ones held.
func (s *Server) confirmDeletes(w http.ResponseWriter, r *http.Request) {
	var body pathsBody
	if !decode(w, r, &body) {
		return
	}
	if body.Paths == nil {
		writeError(w, http.StatusBadRequest, "paths must list the held deletes you're confirming")
		return
	}
	paths, err := s.Node.ConfirmDeletes(r.PathValue("id"), body.Paths)
	if err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pathsBody{Paths: orEmpty(paths)})
}

// Restored lists the held deletes that were withdrawn. Warning says why
// some files couldn't be put back yet; they come back from a peer at the
// next sync.
type Restored struct {
	Paths   []string `json:"paths"`
	Warning string   `json:"warning,omitempty"`
}

// restoreDeletes puts all of a library's held deletes back, so it takes no
// body.
func (s *Server) restoreDeletes(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	paths, err := s.Node.RestoreDeletes(r.PathValue("id"))
	if err != nil && len(paths) == 0 {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	out := Restored{Paths: orEmpty(paths)}
	if err != nil {
		out.Warning = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// versions lists the versions the vault keeps of a library's files, newest
// first: every file's, or with ?path=, one file's.
func (s *Server) versions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Has("path") && q.Get("path") == "" {
		writeError(w, http.StatusBadRequest, "path is empty; leave it out to list every file's versions")
		return
	}
	vs, err := s.Node.Versions(r.PathValue("id"), q.Get("path"))
	if err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

type restoreBody struct {
	Path string `json:"path"`
	// Version is the ID of the version to restore; without it, the newest.
	Version string `json:"version,omitempty"`
}

// restoreVersion puts a version of a file back. What it replaces is kept as
// a version in turn.
func (s *Server) restoreVersion(w http.ResponseWriter, r *http.Request) {
	var body restoreBody
	if !decode(w, r, &body) {
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	ver, err := s.Node.RestoreVersion(r.PathValue("id"), body.Path, body.Version)
	if err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restored": ver})
}

// Offer is what another device needs to pair with this one.
type Offer struct {
	Code     string `json:"code"`
	Token    string `json:"token"`
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	// Fingerprint is this device's key fingerprint. The joining device
	// passes it along so that it accepts no other key.
	Fingerprint string `json:"fingerprint"`
	// Addresses are where this device listens on the local network.
	Addresses []string `json:"addresses"`
	// QR is a synk://pair link that carries all of the above.
	QR        string    `json:"qr"`
	Listen    string    `json:"listen,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// StartPairing arms n to accept one device, for pairing.DefaultTTL.
func StartPairing(n *engine.Node) (Offer, error) {
	sess, err := pairing.NewSession()
	if err != nil {
		return Offer{}, err
	}
	if err := n.ArmPairing(sess.Code, sess.Token); err != nil {
		return Offer{}, err
	}
	p, ok := n.PairingOffer()
	if !ok {
		return Offer{}, errors.New("api: the pairing offer expired as it was made")
	}
	return offerFor(n, p), nil
}

func offerFor(n *engine.Node, p pairing.Pending) Offer {
	l := pairing.Link{Code: p.Code, Token: p.Token, Fingerprint: n.Fingerprint(), Addrs: orEmpty(n.LANAddresses())}
	return Offer{
		Code:        p.Code,
		Token:       p.Token,
		DeviceID:    n.Device.DeviceID,
		Name:        n.Device.Name,
		Fingerprint: l.Fingerprint,
		Addresses:   l.Addrs,
		QR:          l.URI(),
		Listen:      n.BoundAddr(),
		ExpiresAt:   p.ExpiresAt,
	}
}

func (s *Server) pairStart(w http.ResponseWriter, r *http.Request) {
	offer, err := StartPairing(s.Node)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offer)
}

type joinBody struct {
	Addr  string `json:"addr"`
	Code  string `json:"code"`
	Token string `json:"token"`
	// Fingerprint is the offer's; with it, no other device can answer.
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (s *Server) pairJoin(w http.ResponseWriter, r *http.Request) {
	var body joinBody
	if !decode(w, r, &body) {
		return
	}
	if body.Addr == "" || body.Code == "" || body.Token == "" {
		writeError(w, http.StatusBadRequest, "addr, code, and token are required")
		return
	}
	if err := s.Node.JoinPeer(body.Addr, body.Code, body.Token, body.Fingerprint); err != nil {
		writeEngineError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": s.Node.Status()})
}

// pairCode returns the offer waiting for a device to join, if it hasn't
// expired or been used.
func (s *Server) pairCode(w http.ResponseWriter, r *http.Request) {
	p, ok := s.Node.PairingOffer()
	if !ok {
		writeError(w, http.StatusNotFound, "no pairing offer is waiting")
		return
	}
	writeJSON(w, http.StatusOK, offerFor(s.Node, p))
}

type syncBody struct {
	Addr     string `json:"addr"`
	FolderID string `json:"folderId"`
}

// sync syncs one library with one peer, or with an empty body, everything.
func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	var body syncBody
	if !decode(w, r, &body) {
		return
	}
	if (body.Addr == "") != (body.FolderID == "") {
		writeError(w, http.StatusBadRequest, "give both addr and folderId, or neither")
		return
	}
	var err error
	if body.Addr != "" {
		err = s.Node.SyncWith(body.Addr, body.FolderID)
	} else {
		err = s.Node.SyncAllPeers()
	}
	if err != nil {
		writeEngineError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": s.Node.Status()})
}

// Report is what's wrong, if anything, with a device's libraries.
type Report struct {
	OK     bool          `json:"ok"`
	Issues []string      `json:"issues"`
	Status engine.Status `json:"status"`
}

// Diagnose lists what keeps a device's libraries from syncing. Each issue
// starts with the library's label, and its ID if another library has the
// same label.
func Diagnose(st engine.Status) Report {
	labels := map[string]int{}
	for _, l := range st.Libraries {
		labels[l.Label]++
	}
	issues := []string{}
	for _, l := range st.Libraries {
		name := l.Label
		if labels[name] > 1 {
			name += " (" + l.ID + ")"
		}
		say := func(format string, args ...any) {
			issues = append(issues, name+": "+fmt.Sprintf(format, args...))
		}
		switch _, err := os.Stat(l.Path); {
		case l.Error != "":
			say("%s", l.Error)
		case err != nil:
			say("folder missing")
		}
		if len(l.HeldDeletes) > 0 {
			say("%d deletes are held until you confirm or restore them", len(l.HeldDeletes))
		}
		switch n := len(l.ScanErrors); {
		case n == 1:
			say("can't read %s, so it doesn't sync", l.ScanErrors[0])
		case n > 1:
			say("can't read %d files, so they don't sync. The first is %s", n, l.ScanErrors[0])
		}
		switch n := len(l.Placeholders); {
		case n == 1:
			say("%s isn't downloaded to this device, so it doesn't sync until it is", l.Placeholders[0])
		case n > 1:
			say("%d files aren't downloaded to this device, so they don't sync until they are. The first is %s", n, l.Placeholders[0])
		}
		for _, d := range l.Devices {
			switch {
			case d.Failures == 1:
				say("the last sync with %s failed: %s", d.Name, d.Error)
			case d.Failures > 1:
				say("the last %d syncs with %s failed: %s", d.Failures, d.Name, d.Error)
			}
		}
	}
	return Report{OK: len(issues) == 0, Issues: issues, Status: st}
}

// pauseLibrary stops syncing a library until it's resumed. Its files
// stay as they are.
func (s *Server) pauseLibrary(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	id := r.PathValue("id")
	if err := s.Node.PauseLibrary(id); err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	lib, _ := s.Node.Library(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "library": lib})
}

// resumeLibrary starts a paused library syncing again.
func (s *Server) resumeLibrary(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	id := r.PathValue("id")
	if err := s.Node.ResumeLibrary(id); err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	lib, _ := s.Node.Library(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "library": lib})
}

// removeLibrary forgets a library. Synk keeps every file, the library's
// .synk marker, the versions in the vault, and the index, so enabling
// the folder again picks up where it left off.
func (s *Server) removeLibrary(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	if err := s.Node.RemoveLibrary(r.PathValue("id")); err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// unpair forgets a paired device: every library stops sharing with it,
// and its connections are refused. Nothing on disk changes.
func (s *Server) unpair(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	if err := s.Node.Unpair(r.PathValue("id")); err != nil {
		writeEngineError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": s.Node.Status()})
}

// events streams what happens on this device as it happens, one JSON
// event per `data:` line, with a comment now and then to keep the
// connection alive. A reader that falls behind loses events rather
// than slowing the daemon down.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ch, cancel := s.Node.Subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return
	}
	_, _ = io.WriteString(w, "retry: 3000\n\n")
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			b, err := json.Marshal(e)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Diagnose(s.Node.Status()))
}

type nameBody struct {
	Name string `json:"name"`
}

// setDeviceName updates the device's human-readable name. The identity
// keypair and device ID are unchanged; only the label changes.
func (s *Server) setDeviceName(w http.ResponseWriter, r *http.Request) {
	var body nameBody
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	s.Node.Config.DeviceName = name
	if err := config.Save(s.Node.DataDir, s.Node.Config); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save name: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// setRelay stores or clears the relay URL the node dials when direct
// connections fail. An empty URL clears it. The URL is validated against
// the same rules the relay client dials by, so a URL that is saved always
// parses.
func (s *Server) setRelay(w http.ResponseWriter, r *http.Request) {
	var body relayBody
	if !decode(w, r, &body) {
		return
	}
	relayURL := strings.TrimSpace(body.Relay)
	if err := relay.ValidateRelayURL(relayURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Node.Config.Relay = relayURL
	if err := config.Save(s.Node.DataDir, s.Node.Config); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save relay: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"relay": relayURL})
}

// enableLogin registers the bundled engine as a login item.
func (s *Server) enableLogin(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	if err := s.Node.EnableLogin(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
}

// getLogin checks whether the login item is enabled.
func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	enabled, err := s.Node.IsLoginEnabled()
	if err != nil {
		enabled = false
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

// disableLogin removes the login item.
func (s *Server) disableLogin(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, struct{}{}) {
		return
	}
	if err := s.Node.DisableLogin(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": false})
}

// decode reads a JSON body into v. An empty body leaves v unchanged; unknown
// fields and trailing data are errors, so a misspelled field isn't ignored.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if dec.Decode(new(json.RawMessage)) != io.EOF {
			err = errors.New("the body must be one JSON value")
		}
	} else if err == io.EOF {
		err = nil
	}
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	} else {
		writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())
	}
	return false
}

// writeEngineError replies with the status that fits err, or fallback.
func writeEngineError(w http.ResponseWriter, err error, fallback int) {
	code := fallback
	switch {
	case errors.Is(err, engine.ErrUnknownLibrary), errors.Is(err, vault.ErrNoVersion), errors.Is(err, engine.ErrUnknownPeer):
		code = http.StatusNotFound
	case errors.Is(err, engine.ErrInvalidLibrary), errors.Is(err, engine.ErrLibraryPath), errors.Is(err, engine.ErrPairingRejected),
		errors.Is(err, engine.ErrInvalidPath):
		code = http.StatusBadRequest
	case errors.Is(err, engine.ErrLibraryExists), errors.Is(err, engine.ErrHeldChanged), errors.Is(err, engine.ErrLibraryMissing),
		errors.Is(err, engine.ErrNotPaired), errors.Is(err, engine.ErrNotShared), errors.Is(err, engine.ErrPaused),
		errors.Is(err, engine.ErrRefused), errors.Is(err, apply.ErrChangedSinceScan):
		code = http.StatusConflict
	case errors.Is(err, engine.ErrDamagedVersion):
		code = http.StatusUnprocessableEntity
	}
	writeError(w, code, err.Error())
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// guard admits only requests that one of this user's own programs made on
// purpose:
//   - from a loopback address;
//   - to a loopback name, so a DNS-rebound page can't read replies;
//   - without an Origin header, which browsers always send cross-origin;
//   - with the bearer token, except for /v1/health;
//   - for POST, with a JSON body, which a page can't send without a CORS
//     preflight, and preflights carry an Origin.
func guard(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasOrigin := r.Header["Origin"]
		if !loopbackClient(r.RemoteAddr) || !loopbackHost(r.Host) || hasOrigin {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if r.URL.Path != "/v1/health" && !authorized(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="synk"`)
			writeError(w, http.StatusUnauthorized, "missing or wrong API token; it's in "+TokenFile+" in Synk's data folder")
			return
		}
		if r.Method == http.MethodPost {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "POST bodies must be application/json")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Synk", "1")
		next.ServeHTTP(w, r)
	})
}

func authorized(r *http.Request, token string) bool {
	scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return ok && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func loopbackClient(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackHost reports whether a Host header names this device.
func loopbackHost(hostport string) bool {
	host := strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return loopbackName(host)
}

func loopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
