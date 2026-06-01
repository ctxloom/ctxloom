package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
	if cfg == nil || len(cfg.GetBundleDirs()) == 0 {
		return nil, fmt.Errorf("no bundles directory found")
	}
	bundle, err := bundles.NewLoader(cfg.GetBundleDirs(), false).Load(req.Name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	srcData, err := os.ReadFile(bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}

	var dest string
	switch {
	case req.OutputFile != "":
		dest = req.OutputFile
		if dir := filepath.Dir(dest); dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create destination directory: %w", err)
			}
		}
	case req.DestDir != "":
		if err := os.MkdirAll(req.DestDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create destination directory: %w", err)
		}
		dest = filepath.Join(req.DestDir, filepath.Base(bundle.Path))
	default:
		return nil, fmt.Errorf("either an output file or a destination directory must be specified")
	}

	if err := os.WriteFile(dest, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}
	return &ExportBundleResult{Status: "exported", Name: req.Name, Source: bundle.Path, Dest: dest}, nil
}

// ImportBundleRequest is the input for ImportBundle.
type ImportBundleRequest struct {
	SourcePath string `json:"source_path"`
	Force      bool   `json:"force"`
}

// ImportBundleResult reports the import plus a small summary of the bundle.
type ImportBundleResult struct {
	Status    string `json:"status"`
	Source    string `json:"source"`
	Dest      string `json:"dest"`
	Version   string `json:"version"`
	Fragments int    `json:"fragments"`
	Prompts   int    `json:"prompts"`
	MCP       int    `json:"mcp"`
}

// ImportBundle validates a bundle file and copies it into the project's bundles
// directory (symlink-guarded, like CreateBundle). Refuses to overwrite without
// Force.
func ImportBundle(_ context.Context, cfg *config.Config, req ImportBundleRequest) (*ImportBundleResult, error) {
	if cfg == nil || len(cfg.AppPaths) == 0 {
		return nil, fmt.Errorf("no .ctxloom directory configured")
	}
	srcData, err := os.ReadFile(req.SourcePath)
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
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bundles directory: %w", err)
	}
	if _, err := os.Stat(destPath); err == nil && !req.Force {
		return nil, fmt.Errorf("bundle already exists: %s (use --force to overwrite)", destPath)
	}
	if err := os.WriteFile(destPath, srcData, 0644); err != nil {
		return nil, fmt.Errorf("failed to write bundle: %w", err)
	}

	return &ImportBundleResult{
		Status:    "imported",
		Source:    req.SourcePath,
		Dest:      destPath,
		Version:   bundle.Version,
		Fragments: len(bundle.Fragments),
		Prompts:   len(bundle.Prompts),
		MCP:       len(bundle.MCP),
	}, nil
}
