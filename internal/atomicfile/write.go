package atomicfile

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashReader hashes r and returns hex SHA-256. It consumes r.
func HashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// TempPrefix starts the names of WriteFile's temp files.
const TempPrefix = ".synk-tmp-"

// WriteFile writes data to path via a sibling temp file, fsyncs, then rename.
// The previous file, if any, is left untouched until rename succeeds.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, TempPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

// DiskAvailable returns free bytes on the volume containing path.
func DiskAvailable(path string) (uint64, error) {
	return diskAvailable(path)
}

// SameVolume reports whether a and b are on the same filesystem. When that
// can't be determined it reports true, so callers add the space they need.
func SameVolume(a, b string) bool {
	return sameVolume(a, b)
}
