package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

type File struct {
	Version    int    `json:"version"`
	DeviceName string `json:"deviceName"`
	Listen     string `json:"listen"`
	// AllowInternetPeers accepts peer connections from addresses outside
	// the local network. Only paired devices can sync either way.
	AllowInternetPeers bool      `json:"allowInternetPeers,omitempty"`
	API                string    `json:"api"`
	Libraries          []Library `json:"libraries"`
	Peers              []Peer    `json:"peers"`
	Relay              string    `json:"relay,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type Library struct {
	ID     string `json:"id"`
	Preset string `json:"preset"`
	Label  string `json:"label"`
	Path   string `json:"path"`
	Paused bool   `json:"paused"`
	// ShareWith lists the device IDs the library syncs with. Empty means
	// every paired device.
	ShareWith []string `json:"shareWith,omitempty"`
}

type Peer struct {
	DeviceID  string `json:"deviceId"`
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
	// Addresses are tried in order; the last one that worked comes first.
	Addresses []string  `json:"addresses,omitempty"`
	PairedAt  time.Time `json:"pairedAt"`
	// LastSeen is when the device last started a sync session with this
	// one. It's left out until then.
	LastSeen time.Time `json:"lastSeen,omitzero"`
}

func Path(dataDir string) string {
	return filepath.Join(dataDir, "config.json")
}

func Load(dataDir string) (*File, error) {
	b, err := os.ReadFile(Path(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			now := time.Now().UTC()
			return &File{Version: 1, Listen: "0.0.0.0:7420", API: "127.0.0.1:7421", CreatedAt: now, UpdatedAt: now}, nil
		}
		return nil, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Listen == "" {
		f.Listen = "0.0.0.0:7420"
	}
	if f.API == "" {
		f.API = "127.0.0.1:7421"
	}
	return &f, nil
}

func Save(dataDir string, f *File) error {
	f.UpdatedAt = time.Now().UTC()
	if f.Version == 0 {
		f.Version = 1
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(Path(dataDir), b, 0o600)
}

func (f *File) LibraryByID(id string) *Library {
	for i := range f.Libraries {
		if f.Libraries[i].ID == id {
			return &f.Libraries[i]
		}
	}
	return nil
}

func (f *File) PeerByID(id string) *Peer {
	for i := range f.Peers {
		if f.Peers[i].DeviceID == id {
			return &f.Peers[i]
		}
	}
	return nil
}

func (f *File) UpsertPeer(p Peer) {
	for i := range f.Peers {
		if f.Peers[i].DeviceID == p.DeviceID {
			f.Peers[i] = p
			return
		}
	}
	f.Peers = append(f.Peers, p)
}

func (f *File) UpsertLibrary(l Library) {
	for i := range f.Libraries {
		if f.Libraries[i].ID == l.ID {
			f.Libraries[i] = l
			return
		}
	}
	f.Libraries = append(f.Libraries, l)
}

// RemoveLibrary forgets the library with the ID, and says whether there
// was one.
func (f *File) RemoveLibrary(id string) bool {
	for i := range f.Libraries {
		if f.Libraries[i].ID == id {
			f.Libraries = append(f.Libraries[:i], f.Libraries[i+1:]...)
			return true
		}
	}
	return false
}

// RemovePeer forgets the device with the ID, and says whether there was
// one.
func (f *File) RemovePeer(id string) bool {
	for i := range f.Peers {
		if f.Peers[i].DeviceID == id {
			f.Peers = append(f.Peers[:i], f.Peers[i+1:]...)
			return true
		}
	}
	return false
}
