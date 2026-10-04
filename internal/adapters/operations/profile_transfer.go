package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// loadLocalProfile loads a profile for the edit/export flow, which works on a
// LOCAL bundle's profile file: a remote bundle's profile is a read-only
// reference with no file here, edited at its source.
func loadLocalProfile(cfg *config.Config, name string) (*profiles.Profile, error) {
	profile, err := cfg.GetProfileLoader().Load(name)
	if err != nil {
		// Verbatim: the loader's error carries the errs.ErrProfileNotFound
		// sentinel and its actionable detail; re-wrapping flat discards both.
		return nil, err
	}
	if profiles.IsSeededPath(profile.Path) {
		return nil, fmt.Errorf("profile %q is a remote bundle's profile and read-only; edit it at its source and run 'ctxloom deps pull'", name)
	}
	return profile, nil
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
	profile, err := loadLocalProfile(cfg, req.Name)
	if err != nil {
		return nil, err
	}
	srcData, err := afero.ReadFile(fs, profile.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile: %w", err)
	}
	if _, err := decodeWritableProfile(srcData, "profile"); err != nil {
		return nil, fmt.Errorf("cannot export %q: %w", req.Name, err)
	}
	if err := fs.MkdirAll(req.DestDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %w", err)
	}
	dest := filepath.Join(req.DestDir, filepath.Base(profile.Path))
	// No AllowEmpty: decodeWritableProfile above already refuses a hollow
	// document.
	if err := safefs.WriteFile(fs, dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write profile: %w", err)
	}
	return &ExportProfileResult{Status: "exported", Name: req.Name, Source: profile.Path, Dest: dest}, nil
}

// ImportProfileRequest is the input for ImportProfile.
type ImportProfileRequest struct {
	SourcePath string `json:"source_path"`
	Force      bool   `json:"force"`
	// Bundle is the LOCAL bundle the profile is imported into; empty is the
	// project bundle.
	Bundle string `json:"bundle,omitempty"`

	// FS is an optional filesystem the source is read from (defaults to the
	// OS filesystem).
	FS afero.Fs `json:"-"`
}

// ImportProfileResult reports the import.
type ImportProfileResult struct {
	Status string `json:"status"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

// ImportProfile validates a profile YAML file and writes it into a local
// bundle — the project bundle unless Bundle names another — as the profile
// named by the file's basename, refusing to overwrite without Force.
func ImportProfile(_ context.Context, cfg *config.Config, req ImportProfileRequest) (*ImportProfileResult, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	// The profile's name is the source basename, and a bundle's profile item
	// is a .yaml file — anything else imports to a name nothing ever reads,
	// same class as ImportBundle.
	if err := requireLoadableName(req.SourcePath, "profile", ".yaml"); err != nil {
		return nil, err
	}
	srcData, err := afero.ReadFile(getFS(req.FS), req.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}
	// Validate it decodes as a profile AND carries something before writing
	// (catches a malformed or hollow file at import time rather than at the
	// next run).
	profile, err := decodeWritableProfile(srcData, "profile file")
	if err != nil {
		return nil, err
	}
	name := bundleProfileName(req.Bundle, strings.TrimSuffix(filepath.Base(req.SourcePath), ".yaml"))
	if err := prepareLocalBundleWrite(cfg, name); err != nil {
		return nil, err
	}
	loader := cfg.GetProfileLoader()
	if existing, err := loader.Load(name); err == nil {
		if !req.Force {
			return nil, fmt.Errorf("profile already exists: %s (use --force to overwrite)", existing.Path)
		}
		profile.Path = existing.Path
	}
	profile.Name = name
	if err := loader.Save(profile); err != nil {
		return nil, fmt.Errorf("failed to write profile: %w", err)
	}
	return &ImportProfileResult{Status: "imported", Source: req.SourcePath, Dest: profile.Path}, nil
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
	profile, err := loadLocalProfile(cfg, req.Name)
	if err != nil {
		return nil, err
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
// file. A document that would not load is rejected so a botched edit doesn't
// corrupt the profile.
func SetProfileContent(_ context.Context, cfg *config.Config, req SetProfileContentRequest) (*SetProfileContentResult, error) {
	fs := getFS(req.FS)
	profile, err := loadLocalProfile(cfg, req.Name)
	if err != nil {
		return nil, err
	}
	if _, err := decodeWritableProfile([]byte(req.Content), "profile"); err != nil {
		return nil, err
	}
	if err := prepareLocalBundleWrite(cfg, profile.Name); err != nil {
		return nil, err
	}
	// No AllowEmpty: decodeWritableProfile above already refuses a hollow
	// document, so an empty edit is rejected before this write is reached.
	if err := safefs.WriteFile(fs, profile.Path, []byte(req.Content), 0644); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}
	return &SetProfileContentResult{Status: "updated", Name: req.Name, Path: profile.Path}, nil
}
