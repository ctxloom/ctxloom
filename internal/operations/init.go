package operations

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/resources"
)

// InitializeProjectRequest is the input for InitializeProject.
type InitializeProjectRequest struct {
	AppDir string `json:"app_dir"`
	Engine string `json:"engine"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// InitializeProjectResult reports the bootstrap.
type InitializeProjectResult struct {
	Status string `json:"status"`
	AppDir string `json:"app_dir"`
}

// InitializeProject creates the .ctxloom skeleton (dir tree + config.yaml
// carrying the chosen engine + default remotes.yaml). Safe to re-run:
// directories use MkdirAll and files are overwritten.
func InitializeProject(_ context.Context, req InitializeProjectRequest) (*InitializeProjectResult, error) {
	if req.AppDir == "" {
		return nil, fmt.Errorf("app dir is required")
	}
	fs := getFS(req.FS)
	for _, dir := range []string{req.AppDir, filepath.Join(req.AppDir, paths.ProfilesDir), paths.BundlesPath(req.AppDir)} {
		if err := fs.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	if err := afero.WriteFile(fs, paths.ConfigPath(req.AppDir), GenerateConfigYAML(req.Engine), 0644); err != nil {
		return nil, fmt.Errorf("failed to create config.yaml: %w", err)
	}

	remotesContent, err := resources.GetDefaultRemotes()
	if err != nil {
		return nil, fmt.Errorf("failed to read default remotes: %w", err)
	}
	if err := afero.WriteFile(fs, paths.RemotesPath(req.AppDir), remotesContent, 0644); err != nil {
		return nil, fmt.Errorf("failed to create remotes.yaml: %w", err)
	}

	return &InitializeProjectResult{Status: "initialized", AppDir: req.AppDir}, nil
}

// GenerateConfigYAML renders the initial config.yaml for a freshly initialized
// project, with the selected engine wired in as the sole plugin and default.
func GenerateConfigYAML(engine string) []byte {
	return []byte(fmt.Sprintf(`# ctxloom Configuration
# See https://github.com/ctxloom/ctxloom for documentation

# Language model plugin configuration
llm:
  plugins:
    %s: {}

# Default settings
defaults:
  llm_plugin: %s
  use_distilled: true

# MCP server configuration
mcp:
  auto_register_ctxloom: true
`, engine, engine))
}
