package projectid

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// ReadMarker reads the in-tree project-id marker at
// <projectDir>/.ctxloom/project-id. Returns "" (no error) when the marker is
// absent, so callers can treat "missing" and "present" uniformly. A genuine
// read error (e.g. the marker path is unreadable) is returned as-is.
func ReadMarker(projectDir string) (string, error) {
	data, err := os.ReadFile(paths.ProjectMarkerPath(projectDir))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// WriteMarker writes id into <projectDir>/.ctxloom/project-id, creating the
// .ctxloom directory if needed. The marker is private working state and is
// gitignored by `ctxloom init`.
func WriteMarker(projectDir, id string) error {
	path := paths.ProjectMarkerPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id+"\n"), 0o644)
}
