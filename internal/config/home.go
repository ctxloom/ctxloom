package config

import (
	"os"
	"path/filepath"
)

// HomeConfigDir returns the user's home ctxloom directory (~/.ctxloom).
//
// This is the STAGE-1 bootstrap primitive for the HOME side of value
// layering (home < project < env < CLI — see internal/shared/layerconfig):
// resolveConfigLayerPaths calls it to find home's config.yaml so
// loadLayeredConfig can read it as the lower-precedence layer underneath
// whatever project config.yaml findAppDir resolved. It does no filesystem
// I/O itself (a pure path join, matching the other Home*Path helpers in
// internal/paths) — callers decide whether/how to read what's there.
func HomeConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName), nil
}
