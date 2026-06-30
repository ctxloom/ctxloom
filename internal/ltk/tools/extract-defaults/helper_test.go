package main

import (
	"os"
	"path/filepath"
)

// readUp reads rel by walking up from the test's working directory to the
// module root (the dir containing go.mod), so tests work regardless of the
// per-package cwd `go test` uses.
func readUp(rel string) ([]byte, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return os.ReadFile(filepath.Join(dir, rel))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return os.ReadFile(rel) // fall back to cwd-relative
		}
		dir = parent
	}
}
