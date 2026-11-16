package login

import (
	"os/exec"
	"path/filepath"
)

// Enable registers the app bundle at bundlePath as a login item.
// It uses the macOS defaults mechanism, which works for any bundle.
func Enable(bundlePath string) error {
	cmd := exec.Command("defaults", "write", "com.apple.loginitems",
		"org.synk.mac", "-dict", "Path", bundlePath, "Hide", "1")
	return cmd.Run()
}

// Disable removes the app bundle from login items.
func Disable(bundlePath string) error {
	cmd := exec.Command("defaults", "delete", "com.apple.loginitems",
		"org.synk.mac")
	return cmd.Run()
}

// IsEnabled checks whether the app bundle is registered as a login item.
func IsEnabled(bundlePath string) (bool, error) {
	cmd := exec.Command("defaults", "read", "com.apple.loginitems",
		"org.synk.mac")
	out, err := cmd.Output()
	if err != nil {
		return false, nil // not registered
	}
	_ = out
	return true, nil
}

// BundlePath returns the path to the synk-daemon binary inside the app bundle.
func BundlePath(appBundle string) string {
	return filepath.Join(appBundle, "Contents", "MacOS", "synk-daemon")
}
