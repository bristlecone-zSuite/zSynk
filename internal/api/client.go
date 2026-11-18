package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/bristlecone-zSuite/zSynk/internal/config"
	"github.com/bristlecone-zSuite/zSynk/internal/engine"
	"github.com/bristlecone-zSuite/zSynk/internal/vault"
)

// Client calls the local API of the daemon that has a data folder open.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// Connect returns a client for the API at addr, authenticated with the
// token in dataDir.
func Connect(dataDir, addr string) (*Client, error) {
	tok, err := ReadToken(dataDir)
	if err != nil {
		return nil, err
	}
	return &Client{base: "http://" + addr, token: tok, http: &http.Client{}}, nil
}

// Error is a reply other than 2xx.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

func (c *Client) Status() (engine.Status, error) {
	var st engine.Status
	err := c.do(http.MethodGet, "/v1/status", nil, &st)
	return st, err
}

// SetRelay stores or clears (empty string) the daemon's relay URL.
func (c *Client) SetRelay(relayURL string) (string, error) {
	var out relayBody
	err := c.do(http.MethodPost, "/v1/config/relay", relayBody{Relay: relayURL}, &out)
	return out.Relay, err
}

type relayBody struct {
	Relay string `json:"relay"`
}

// EnableVaultEncryption seals the node's vault and blobs under a key
// derived from the passphrase. DisableVaultEncryption unseals them.
// Both refuse when the node's state doesn't match the request.
func (c *Client) EnableVaultEncryption(passphrase string) error {
	return c.do(http.MethodPost, "/v1/config/encryption/enable", passphraseBody{Passphrase: passphrase}, nil)
}

func (c *Client) DisableVaultEncryption(passphrase string) error {
	return c.do(http.MethodPost, "/v1/config/encryption/disable", passphraseBody{Passphrase: passphrase}, nil)
}

type passphraseBody struct {
	Passphrase string `json:"passphrase"`
}

func (c *Client) EnableLibrary(l config.Library) (config.Library, error) {
	var out config.Library
	err := c.do(http.MethodPost, "/v1/libraries", l, &out)
	return out, err
}

func (c *Client) StartPairing() (Offer, error) {
	var out Offer
	err := c.do(http.MethodPost, "/v1/pair/start", nil, &out)
	return out, err
}

func (c *Client) JoinPeer(addr, code, token, fingerprint string) error {
	return c.do(http.MethodPost, "/v1/pair/join", joinBody{Addr: addr, Code: code, Token: token, Fingerprint: fingerprint}, nil)
}

// PairingOffer returns the offer waiting for a device to pair.
func (c *Client) PairingOffer() (Offer, error) {
	var out Offer
	err := c.do(http.MethodGet, "/v1/pair/code", nil, &out)
	return out, err
}

// Sync syncs one library with the peer at addr, or with both empty, every
// library with every peer.
func (c *Client) Sync(addr, folderID string) error {
	return c.do(http.MethodPost, "/v1/sync", syncBody{Addr: addr, FolderID: folderID}, nil)
}

// ConfirmDeletes sends the held deletes in paths, which must be all of the
// library's held deletes.
func (c *Client) ConfirmDeletes(id string, paths []string) ([]string, error) {
	var out pathsBody
	err := c.do(http.MethodPost, "/v1/libraries/"+url.PathEscape(id)+"/deletes/confirm", pathsBody{Paths: orEmpty(paths)}, &out)
	return out.Paths, err
}

func (c *Client) RestoreDeletes(id string) (Restored, error) {
	var out Restored
	err := c.do(http.MethodPost, "/v1/libraries/"+url.PathEscape(id)+"/deletes/restore", nil, &out)
	return out, err
}

// Versions lists the versions kept of path in library id, or with path
// empty, of every path in it, newest first.
func (c *Client) Versions(id, path string) ([]vault.Version, error) {
	var out struct {
		Versions []vault.Version `json:"versions"`
	}
	p := "/v1/libraries/" + url.PathEscape(id) + "/versions"
	if path != "" {
		p += "?" + url.Values{"path": {path}}.Encode()
	}
	err := c.do(http.MethodGet, p, nil, &out)
	return out.Versions, err
}

// RestoreVersion puts back a version of path in library id: the one named
// version, or with version empty, the newest.
func (c *Client) RestoreVersion(id, path, version string) (vault.Version, error) {
	var out struct {
		Restored vault.Version `json:"restored"`
	}
	err := c.do(http.MethodPost, "/v1/libraries/"+url.PathEscape(id)+"/versions/restore", restoreBody{Path: path, Version: version}, &out)
	return out.Restored, err
}

// SetLibraryIgnore replaces a library's ignore patterns on the daemon.
// Selective sync is per device: these patterns apply on this device only.
func (c *Client) SetLibraryIgnore(id string, ignore []string) (config.Library, error) {
	var out struct {
		Library config.Library `json:"library"`
	}
	err := c.do(http.MethodPost, "/v1/libraries/"+url.PathEscape(id)+"/ignore", ignoreBody{Ignore: ignore}, &out)
	return out.Library, err
}

type ignoreBody struct {
	Ignore []string `json:"ignore"`
}

// PauseLibrary stops syncing a library until ResumeLibrary. Its files
// stay as they are.
func (c *Client) PauseLibrary(id string) (config.Library, error) {
	var out struct {
		Library config.Library `json:"library"`
	}
	err := c.do(http.MethodPost, "/v1/libraries/"+url.PathEscape(id)+"/pause", nil, &out)
	return out.Library, err
}

// ResumeLibrary starts a paused library syncing again.
func (c *Client) ResumeLibrary(id string) (config.Library, error) {
	var out struct {
		Library config.Library `json:"library"`
	}
	err := c.do(http.MethodPost, "/v1/libraries/"+url.PathEscape(id)+"/resume", nil, &out)
	return out.Library, err
}

// RemoveLibrary forgets a library: Synk keeps its files, its versions,
// and its index, and stops syncing it.
func (c *Client) RemoveLibrary(id string) error {
	return c.do(http.MethodDelete, "/v1/libraries/"+url.PathEscape(id), nil, nil)
}

// Unpair forgets a paired device: every library stops sharing with it,
// and its connections are refused.
func (c *Client) Unpair(id string) error {
	return c.do(http.MethodDelete, "/v1/peers/"+url.PathEscape(id), nil, nil)
}

func (c *Client) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := string(bytes.TrimSpace(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &Error{Status: resp.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
