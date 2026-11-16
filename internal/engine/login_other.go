//go:build !darwin

package engine

func enableLogin(dataDir string) error {
	return nil // login items are macOS-only
}

func disableLogin(dataDir string) error {
	return nil
}

func isLoginEnabled() (bool, error) {
	return false, nil
}
