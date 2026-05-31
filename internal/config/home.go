package config

import (
	"os"
	"path/filepath"
)

// HomeConfigDir returns the user's home ctxloom directory (~/.ctxloom).
//
// Config resolution is project-XOR-home: Load uses a single source and never
// merges. HomeConfigDir is for the cases that need to consult the home config
// independently of that resolution — e.g. inheriting home defaults.profiles
// into a project run that has none of its own.
func HomeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName), nil
}
