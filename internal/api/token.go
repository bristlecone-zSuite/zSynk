package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bristlecone-zSuite/zSynk/internal/atomicfile"
)

// TokenFile is the file in the data folder that holds the bearer token the
// local API requires. Only the user who owns the data folder can read it.
const TokenFile = "api-token"

const tokenBytes = 32

func TokenPath(dataDir string) string {
	return filepath.Join(dataDir, TokenFile)
}

// ReadToken returns the token clients send to the local API.
func ReadToken(dataDir string) (string, error) {
	b, err := os.ReadFile(TokenPath(dataDir))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if !validToken(tok) {
		return "", fmt.Errorf("api: %s doesn't hold an API token", TokenPath(dataDir))
	}
	return tok, nil
}

// LoadOrCreateToken returns the API token, creating one if there isn't a
// usable one. A token file that other users could have read is replaced.
func LoadOrCreateToken(dataDir string) (string, error) {
	p := TokenPath(dataDir)
	if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() && (runtime.GOOS == "windows" || st.Mode().Perm()&0o077 == 0) {
		if tok, err := ReadToken(dataDir); err == nil {
			return tok, nil
		}
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	if err := atomicfile.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

func validToken(tok string) bool {
	if len(tok) != 2*tokenBytes {
		return false
	}
	_, err := hex.DecodeString(tok)
	return err == nil
}
