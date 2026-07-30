package projectid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// ReadMarker reads the in-tree project-id marker at
// <projectDir>/.ctxloom/project-id. Returns "" (no error) when the marker is
// absent, so callers can treat "missing" and "present" uniformly. A genuine
// read error (e.g. the marker path is unreadable) is returned as-is.
func ReadMarker(projectDir string) (string, error) {
	path, err := paths.ProjectMarkerPath(projectDir)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", nil
	}
	// The marker travels with the working tree and can be committed by a
	// third party, so a crafted value must never be adopted as identity or
	// flow into a task-log path. Reject anything that is not a clean,
	// single-segment id here, at the source.
	if err := paths.ValidateProjectID(id); err != nil {
		return "", fmt.Errorf("invalid project marker at %s: %w", projectDir, err)
	}
	return id, nil
}

// WriteMarker writes id into <projectDir>/.ctxloom/project-id, creating the
// .ctxloom directory if needed. The marker is private working state and is
// gitignored by `ctxloom init`.
func WriteMarker(projectDir, id string) error {
	path, err := paths.ProjectMarkerPath(projectDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Atomic write, matching how the registry persists (registry.saveLocked):
	// a crash or concurrent write mid-update must not leave a truncated marker
	// that a later ReadMarker could trim+validate into a different identity.
	return iox.WriteFileAtomic(path, []byte(id+"\n"), 0o644)
}
