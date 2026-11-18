package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bristlecone-zSuite/zSynk/internal/engine"
)

func TestSetDeviceNameEmpty(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodPost, "/v1/config/name", `{"name":""}`)
	req.Header.Del("Authorization")
	if w := serve(s, req); w.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d", w.Code)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if w := serve(s, req); w.Code != http.StatusBadRequest {
		t.Errorf("empty name: got %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func TestSetDeviceNameOK(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodPost, "/v1/config/name", `{"name":"Studio Mac"}`)
	w := serve(s, req)
	if w.Code != http.StatusOK {
		t.Fatalf("name: got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["name"] != "Studio Mac" {
		t.Errorf("name: got %v, want Studio Mac", resp)
	}
	if s.Node.Config.DeviceName != "Studio Mac" {
		t.Errorf("config not updated: %q", s.Node.Config.DeviceName)
	}
}

// TestConfigRelay covers POST /v1/config/relay: a ws or wss URL is saved and
// reported by status, an empty URL clears it, and everything else is
// rejected without touching what was saved before.
func TestConfigRelay(t *testing.T) {
	s := newServer(t, "Mac")

	post := func(body string) int {
		return serve(s, request(s, http.MethodPost, "/v1/config/relay", body)).Code
	}
	if code := post(`{"relay":"wss://relay.example.com"}`); code != http.StatusOK {
		t.Fatalf("set relay: got %d, want 200", code)
	}
	if s.Node.Config.Relay != "wss://relay.example.com" {
		t.Errorf("config not updated: %q", s.Node.Config.Relay)
	}
	var st engine.Status
	if err := json.Unmarshal(serve(s, request(s, http.MethodGet, "/v1/status", "")).Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.Relay != "wss://relay.example.com" {
		t.Errorf("status relay: got %q, want wss://relay.example.com", st.Relay)
	}

	if code := post(`{"relay":""}`); code != http.StatusOK {
		t.Errorf("clear relay: got %d, want 200", code)
	}
	if s.Node.Config.Relay != "" {
		t.Errorf("relay not cleared: %q", s.Node.Config.Relay)
	}

	// Every rejection below must leave the (already cleared) config alone.
	for _, body := range []string{
		`{"relay":"ftp://relay.example.com"}`,
		`{"relay":"wss://"}`,
		`{"relay":"https://relay.example.com"}`,
		`{bad`,
	} {
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("relay %q: got %d, want 400", body, code)
		}
	}
	if s.Node.Config.Relay != "" {
		t.Errorf("rejected body changed config: %q", s.Node.Config.Relay)
	}
}

func TestGetLogin(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodGet, "/v1/login", "")
	w := serve(s, req)
	if w.Code != http.StatusOK {
		t.Fatalf("getLogin: got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["enabled"]; !ok {
		t.Errorf("missing 'enabled' key in %v", resp)
	}
}

func TestEnableLogin(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodPost, "/v1/login", `{}`)
	w := serve(s, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enableLogin: got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp["enabled"] {
		t.Error("expected enabled=true")
	}
}

func TestDisableLogin(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodDelete, "/v1/login", "")
	w := serve(s, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disableLogin: got %d; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["enabled"] {
		t.Error("expected enabled=false")
	}
}

func TestLoginEndpointsNeedToken(t *testing.T) {
	s := newServer(t, "Mac")

	// GET /v1/login without auth → 401
	req := httptest.NewRequest(http.MethodGet, base+"/v1/login", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	w := serve(s, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("getLogin no auth: got %d, want 401", w.Code)
	}

	// POST /v1/login without auth → 401
	req = httptest.NewRequest(http.MethodPost, base+"/v1/login", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	w = serve(s, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("enableLogin no auth: got %d, want 401", w.Code)
	}

	// DELETE /v1/login without auth → 401
	req = httptest.NewRequest(http.MethodDelete, base+"/v1/login", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	w = serve(s, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("disableLogin no auth: got %d, want 401", w.Code)
	}
}

func TestNameEndpointNeedsToken(t *testing.T) {
	s := newServer(t, "Mac")
	req := httptest.NewRequest(http.MethodPost, base+"/v1/config/name", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	w := serve(s, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("setName no auth: got %d, want 401", w.Code)
	}
}

func TestGetLoginBody(t *testing.T) {
	s := newServer(t, "Mac")
	req := request(s, http.MethodGet, "/v1/login", "")
	w := serve(s, req)
	body := w.Body.Bytes()
	if !strings.Contains(string(body), `"enabled"`) {
		t.Errorf("getLogin body missing 'enabled' key: %s", body)
	}
}
