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
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// profileLoaderFS builds a profile loader over the given filesystem. It resolves
// dirs directly (not via GetProfileDirs, which os.Stat-gates on the real FS) so
// an injected filesystem works; the loader filters non-existent dirs itself.
func profileLoaderFS(cfg *config.Config, fs afero.Fs) *profiles.Loader {
	var dirs []string
	for _, p := range cfg.GetAppPaths() {
		dirs = append(dirs, paths.ProfilesPath(p))
	}
	return profiles.NewLoader(dirs, profiles.WithFS(fs))
}

// loadLocalProfile loads a profile for the edit/export flow, which is
// deliberately local-only: profileLoaderFS seeds no remote resolvers, so a
// remote (locked) profile can't be edited or exported in place. On a load
// failure, distinguish a remote reference — whose "not found" was misleading,
// since it exists, just not as a local file — from a genuinely absent local
// profile, and point the user at pulling it first.
func loadLocalProfile(cfg *config.Config, fs afero.Fs, name string) (*profiles.Profile, error) {
	profile, err := profileLoaderFS(cfg, fs).Load(name)
	if err != nil {
		if isRemoteReference(name) {
			return nil, fmt.Errorf("profile %q is a remote reference; the edit/export flow is local-only (pull it first, then edit the local copy)", name)
		}
		// Verbatim: the loader's error carries the errs.ErrProfileNotFound
		// sentinel and its actionable detail; re-wrapping flat discards both.
		return nil, err
	}
	return profile, nil
}

// validateProfileDocument rejects a profile document that carries nothing at all.
//
// yaml.v3 accepts "", whitespace, a comment-only file and `null` into a
// zero-valued profile with a nil error — only a bare scalar errors. So "does it
// parse?" was never the question the write paths thought they were asking, and
// all three of them (import, edit-write-back, export) treated a document that
// carries NOTHING as a valid profile: `profile edit` truncated a real profile to
// zero bytes and reported "updated", `profile import` accepted a hollow file as
// a profile, and `profile export` shipped a hollow one out for someone else to
// discover.
//
// The refusal line is profiles.Profile.IsEmptyDocument — the SAME line
// profiles.Loader.Save draws, so the four write paths cannot drift apart. A
// labels-only profile is deliberately above that line: it is a normal
// half-authored state, it saves, and the fail-loudly gate (which Load and Save
// already trip) says what it will not do.
func validateProfileDocument(data []byte, what string) (*profiles.Profile, error) {
	var probe profiles.Profile
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", what, err)
	}
	if probe.IsEmptyDocument() {
		return nil, fmt.Errorf("refusing to write an empty %s: it carries nothing at all — no parents, bundles, fragments, bundle_items, commands, skills, select_tags, hooks, mcp, variables, llm, description or tags (an empty, whitespace-only or comment-only document parses cleanly, which is why this has to be checked explicitly)", what)
	}
	return &probe, nil
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
	profile, err := loadLocalProfile(cfg, fs, req.Name)
	if err != nil {
		return nil, err
	}
	srcData, err := afero.ReadFile(fs, profile.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read profile: %w", err)
	}
	if _, err := validateProfileDocument(srcData, "profile"); err != nil {
		return nil, fmt.Errorf("cannot export %q: %w", req.Name, err)
	}
	if err := fs.MkdirAll(req.DestDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %w", err)
	}
	dest := filepath.Join(req.DestDir, filepath.Base(profile.Path))
	// No AllowEmpty: validateProfileDocument above already refuses a hollow
	// document.
	if err := iox.WriteFileAtomicFs(fs, dest, srcData, 0644); err != nil {
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
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)
	// The destination basename is the source basename, and the profile scan
	// filters on .yaml/.yml — anything else imports to a path nothing ever
	// reads, same class as ImportBundle.
	if err := requireLoadableName(req.SourcePath, "profile", ".yaml", ".yml"); err != nil {
		return nil, err
	}
	srcData, err := afero.ReadFile(fs, req.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}
	// Validate it parses as a profile AND carries something before copying
	// (catches a malformed or hollow file at import time rather than at the
	// next run).
	if _, err := validateProfileDocument(srcData, "profile file"); err != nil {
		return nil, err
	}

	profileDir := paths.ProfilesPath(cfg.GetAppPaths()[0])
	if err := fs.MkdirAll(profileDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create profiles directory: %w", err)
	}
	dest := filepath.Join(profileDir, filepath.Base(req.SourcePath))
	// The error mattered: a destination that cannot be STATTED (permissions, a
	// broken symlink) used to read as "does not exist" and was overwritten
	// without --force, which is the one outcome the guard exists to prevent.
	exists, err := afero.Exists(fs, dest)
	if err != nil {
		return nil, fmt.Errorf("cannot check whether %s already exists: %w", dest, err)
	}
	if exists && !req.Force {
		return nil, fmt.Errorf("profile already exists: %s (use --force to overwrite)", dest)
	}
	// No AllowEmpty: validateProfileDocument above already refuses a hollow
	// document.
	if err := iox.WriteFileAtomicFs(fs, dest, srcData, 0644); err != nil {
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
	profile, err := loadLocalProfile(cfg, fs, req.Name)
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
// file. Invalid YAML is rejected so a botched edit doesn't corrupt the profile.
func SetProfileContent(_ context.Context, cfg *config.Config, req SetProfileContentRequest) (*SetProfileContentResult, error) {
	fs := getFS(req.FS)
	profile, err := loadLocalProfile(cfg, fs, req.Name)
	if err != nil {
		return nil, err
	}
	if _, err := validateProfileDocument([]byte(req.Content), "profile"); err != nil {
		return nil, err
	}
	// No AllowEmpty: validateProfileDocument above already refuses a hollow
	// document, so an empty edit is rejected before this write is reached.
	if err := iox.WriteFileAtomicFs(fs, profile.Path, []byte(req.Content), 0644); err != nil {
		return nil, fmt.Errorf("failed to save profile: %w", err)
	}
	return &SetProfileContentResult{Status: "updated", Name: req.Name, Path: profile.Path}, nil
}
