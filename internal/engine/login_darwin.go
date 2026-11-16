//go:build darwin

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// appBundle returns the path to the Synk.app bundle that contains this
// binary. It walks up from the executable until it finds a Contents/MacOS
// directory, then returns the parent bundle.
func appBundle() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// Walk up until we find a Contents/MacOS directory.
	dir := filepath.Dir(exe)
	for i := 0; i < 10; i++ {
		if filepath.Base(dir) == "MacOS" && filepath.Base(filepath.Dir(dir)) == "Contents" {
			return filepath.Dir(filepath.Dir(dir))
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func enableLogin(dataDir string) error {
	bundle := appBundle()
	if bundle == "" {
		return nil // not running from a bundle; no login item to set
	}
	cmd := exec.Command("defaults", "write", "com.apple.loginitems",
		"org.synk.mac", "-dict", "Path", bundle, "Hide", "1")
	return cmd.Run()
}

func disableLogin(dataDir string) error {
	cmd := exec.Command("defaults", "delete", "com.apple.loginitems",
		"org.synk.mac")
	if err := cmd.Run(); err != nil {
		// Exit 1 means the key didn't exist — already disabled.
		if e, ok := err.(*exec.ExitError); ok && e.ExitCode() == 1 {
			return nil
		}
		return err
	}
	return nil
}

func isLoginEnabled() (bool, error) {
	cmd := exec.Command("defaults", "read", "com.apple.loginitems",
		"org.synk.mac")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil // not registered
	}
	_ = out
	return true, nil
}
