//go:build darwin

package paths

import (
	"os"
	"path/filepath"
)

// documentsDir is ~/Documents: macOS has no per-user relocation of it.
func documentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents"), nil
}
