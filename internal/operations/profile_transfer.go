package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
)

// profileLoaderFS builds a profile loader over the given filesystem. It resolves
// dirs directly (not via GetProfileDirs, which os.Stat-gates on the real FS) so
// an injected filesystem works; the loader filters non-existent dirs itself.
func profileLoaderFS(cfg *config.Config, fs afero.Fs) *profiles.Loader {
	var dirs []string
	for _, p := range cfg.AppPaths {
		dirs = append(dirs, paths.ProfilesPath(p))
	}
	return profiles.NewLoader(dirs, profiles.WithFS(fs))
}

// ExportProfileRequest is the input for ExportProfile.
type ExportProfileRequest struct {
	Name    string `json:"name"`
	DestDir string `json:"dest_dir"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ExportProfileResult reports the export.
type ExportProfileResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

// ExportProfile copies a named profile out to a directory (e.g. staging for
// publish). The destination is user-chosen, outside the profiles tree.
func ExportProfile(_ context.Context, cfg *config.Config, req ExportProfileRequest) (*ExportProfileResult, error) {
	if req.DestDir == "" {
		return nil, fmt.Errorf("destination directory is required")
	}
	fs := getFS(req.FS)
	profile, err := profileLoaderFS(cfg, fs).Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile %q not found", req.Name)
	}
	srcData, err := afero.ReadFile(fs, profile.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile: %w", err)
	}
	if err := fs.MkdirAll(req.DestDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %w", err)
	}
	dest := filepath.Join(req.DestDir, filepath.Base(profile.Path))
	if err := afero.WriteFile(fs, dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write profile: %w", err)
	}
	return &ExportProfileResult{Status: "exported", Name: req.Name, Source: profile.Path, Dest: dest}, nil
}

// ImportProfileRequest is the input for ImportProfile.
type ImportProfileRequest struct {
	SourcePath string `json:"source_path"`
	Force      bool   `json:"force"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ImportProfileResult reports the import.
type ImportProfileResult struct {
	Status string `json:"status"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

// ImportProfile validates a profile YAML file and copies it into the project's
// profiles directory, refusing to overwrite without Force.
func ImportProfile(_ context.Context, cfg *config.Config, req ImportProfileRequest) (*ImportProfileResult, error) {
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)
	srcData, err := afero.ReadFile(fs, req.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}
	// Validate it parses as a profile before copying (catches a malformed file
	// at import time rather than at the next run).
	var probe config.Profile
	if err := yaml.Unmarshal(srcData, &probe); err != nil {
		return nil, fmt.Errorf("invalid profile file: %w", err)
	}

	profileDir := filepath.Join(cfg.AppPaths[0], "profiles")
	if err := fs.MkdirAll(profileDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create profiles directory: %w", err)
	}
	dest := filepath.Join(profileDir, filepath.Base(req.SourcePath))
	if exists, _ := afero.Exists(fs, dest); exists && !req.Force {
		return nil, fmt.Errorf("profile already exists: %s (use --force to overwrite)", dest)
	}
	if err := afero.WriteFile(fs, dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write profile: %w", err)
	}
	return &ImportProfileResult{Status: "imported", Source: req.SourcePath, Dest: dest}, nil
}

// GetProfileContentRequest / SetProfileContentRequest back the `profile edit`
// flow: the frontend reads the profile's YAML, runs its $EDITOR, and writes the
// result back through the core (which validates and persists).
type GetProfileContentRequest struct {
	Name string `json:"name"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// GetProfileContentResult carries a profile's raw YAML.
type GetProfileContentResult struct {
	Content string `json:"content"`
	Path    string `json:"path"`
}

// GetProfileContent returns a profile's raw YAML file content.
func GetProfileContent(_ context.Context, cfg *config.Config, req GetProfileContentRequest) (*GetProfileContentResult, error) {
	fs := getFS(req.FS)
	profile, err := profileLoaderFS(cfg, fs).Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile %q not found", req.Name)
	}
	data, err := afero.ReadFile(fs, profile.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile: %w", err)
	}
	return &GetProfileContentResult{Content: string(data), Path: profile.Path}, nil
}

// SetProfileContentRequest is the input for SetProfileContent.
type SetProfileContentRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// SetProfileContentResult reports the write.
type SetProfileContentResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Path   string `json:"path"`
}

// SetProfileContent validates edited YAML and writes it back to the profile's
// file. Invalid YAML is rejected so a botched edit doesn't corrupt the profile.
func SetProfileContent(_ context.Context, cfg *config.Config, req SetProfileContentRequest) (*SetProfileContentResult, error) {
	fs := getFS(req.FS)
	profile, err := profileLoaderFS(cfg, fs).Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("profile %q not found", req.Name)
	}
	var probe config.Profile
	if err := yaml.Unmarshal([]byte(req.Content), &probe); err != nil {
		return nil, fmt.Errorf("invalid profile: %w", err)
	}
	if err := afero.WriteFile(fs, profile.Path, []byte(req.Content), 0644); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}
	return &SetProfileContentResult{Status: "updated", Name: req.Name, Path: profile.Path}, nil
}
