package pairing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

const pendingName = "pairing.json"
const DefaultTTL = time.Hour

// Pending is a pairing offer that must survive `synk pair-offer` exiting
// so a later `synk serve` in the same data dir can accept the join.
type Pending struct {
	Code      string    `json:"code"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func PendingPath(dataDir string) string {
	return filepath.Join(dataDir, pendingName)
}

func SavePending(dataDir string, p *Pending) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.ExpiresAt.IsZero() {
		p.ExpiresAt = p.CreatedAt.Add(DefaultTTL)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(PendingPath(dataDir), b, 0o600)
}

// LoadPending returns a live offer, or (nil, nil) if none / expired.
func LoadPending(dataDir string) (*Pending, error) {
	b, err := os.ReadFile(PendingPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.ExpiresAt.IsZero() || time.Now().UTC().After(p.ExpiresAt) {
		_ = ClearPending(dataDir)
		return nil, nil
	}
	if p.Code == "" || p.Token == "" {
		return nil, nil
	}
	return &p, nil
}

func ClearPending(dataDir string) error {
	err := os.Remove(PendingPath(dataDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
