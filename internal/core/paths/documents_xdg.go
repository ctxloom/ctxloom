//go:build !windows && !darwin

package paths

import (
	"os"
	"path/filepath"
)

// documentsDir is the XDG Documents folder: XDG_DOCUMENTS_DIR from
// $XDG_CONFIG_HOME/user-dirs.dirs (default ~/.config), else ~/Documents.
// Read from the file rather than by running xdg-user-dir, which is not
// installed everywhere and would make path resolution spawn a process.
func documentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	if data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs")); err == nil {
		if dir, ok := parseUserDirsDocuments(string(data), home); ok {
			return dir, nil
		}
	}
	return filepath.Join(home, "Documents"), nil
}
