package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// ExportBundleRequest is the input for ExportBundle. Exactly one of OutputFile
// (a target file path) or DestDir (a directory the bundle's filename lands in)
// must be set.
type ExportBundleRequest struct {
	Name       string `json:"name"`
	OutputFile string `json:"output_file,omitempty"`
	DestDir    string `json:"dest_dir,omitempty"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ExportBundleResult reports the export.
type ExportBundleResult struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

// ExportBundle copies a named bundle out to an arbitrary file or directory (an
// author workflow — e.g. staging for publish). The destination is user-chosen
// and outside the bundles tree, so no symlink guard applies.
func ExportBundle(_ context.Context, cfg *config.Config, req ExportBundleRequest) (*ExportBundleResult, error) {
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no bundles directory found")
	}
	fs := getFS(req.FS)
	// Resolve bundle dirs directly (not via GetBundleDirs, which os.Stat-gates
	// on the real FS) so an injected filesystem works; the loader filters
	// non-existent dirs itself via afero.DirExists.
	var dirs []string
	for _, p := range cfg.AppPaths {
		dirs = append(dirs, paths.BundlesPath(p))
	}
	// Accept a per-remote short "<remote>/<bundle>" name (decision E: a local file
	// of the same spelling still wins); bare/canonical names pass through.
	name := canonicalizeBundleArg(cfg, req.Name, dirs, req.FS)
	bundle, err := bundles.NewLoader(dirs, false, bundles.WithFS(fs)).Load(name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	srcData, err := afero.ReadFile(fs, bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}

	var dest string
	switch {
	case req.OutputFile != "":
		dest = req.OutputFile
		if dir := filepath.Dir(dest); dir != "." {
			if err := fs.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create destination directory: %w", err)
			}
		}
	case req.DestDir != "":
		if err := fs.MkdirAll(req.DestDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create destination directory: %w", err)
		}
		dest = filepath.Join(req.DestDir, filepath.Base(bundle.Path))
	default:
		return nil, fmt.Errorf("either an output file or a destination directory must be specified")
	}

	if err := afero.WriteFile(fs, dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}
	return &ExportBundleResult{Status: "exported", Name: req.Name, Source: bundle.Path, Dest: dest}, nil
}

// ImportBundleRequest is the input for ImportBundle.
type ImportBundleRequest struct {
	SourcePath string `json:"source_path"`
	Force      bool   `json:"force"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ImportBundleResult reports the import plus a small summary of the bundle.
type ImportBundleResult struct {
	Status    string `json:"status"`
	Source    string `json:"source"`
	Dest      string `json:"dest"`
	Version   string `json:"version"`
	Fragments int    `json:"fragments"`
	Skills    int    `json:"skills"`
	MCP       int    `json:"mcp"`
}

// ImportBundle validates a bundle file and copies it into the project's bundles
// directory (symlink-guarded, like CreateBundle). Refuses to overwrite without
// Force.
func ImportBundle(_ context.Context, cfg *config.Config, req ImportBundleRequest) (*ImportBundleResult, error) {
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	fs := getFS(req.FS)
	srcData, err := afero.ReadFile(fs, req.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}
	bundle, err := bundles.ParseBundle(srcData)
	if err != nil {
		return nil, fmt.Errorf("invalid bundle file: %w", err)
	}

	bundleDir := paths.BundlesPath(cfg.AppPaths[0])
	destPath := filepath.Join(bundleDir, filepath.Base(req.SourcePath))
	if err := requireSafeBundlePath([]string{bundleDir}, destPath); err != nil {
		return nil, err
	}
	if err := fs.MkdirAll(bundleDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}
	if exists, _ := afero.Exists(fs, destPath); exists && !req.Force {
		return nil, fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}
	if err := afero.WriteFile(fs, destPath, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}

	return &ImportBundleResult{
		Status:    "imported",
		Source:    req.SourcePath,
		Dest:      destPath,
		Version:   bundle.Version,
		Fragments: len(bundle.Fragments),
		Skills:    len(bundle.Skills),
		MCP:       len(bundle.MCP),
	}, nil
}
