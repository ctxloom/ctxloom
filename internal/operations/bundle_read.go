package operations

import (
	"context"
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// ReadBundleRequest is the input for ReadBundle.
type ReadBundleRequest struct {
	Name string `json:"name"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ReadBundleResult carries the loaded bundle and its raw YAML bytes.
type ReadBundleResult struct {
	Bundle *bundles.Bundle `json:"-"`
	Raw    []byte          `json:"-"`
}

// ReadBundle loads a bundle by name and returns it along with its raw YAML
// content — the read half behind `bundle view`. The frontend renders; the file
// read goes through the library.
func ReadBundle(_ context.Context, cfg *config.Config, req ReadBundleRequest) (*ReadBundleResult, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return nil, fmt.Errorf("no bundles directory found")
	}
	fs := getFS(req.FS)
	// The authored (committed content) bundle dirs, resolved directly rather
	// than via GetBundleDirs so an injected filesystem works — GetBundleDirs
	// os.Stat-gates on the real FS. The loader filters non-existent dirs itself.
	var dirs []string
	for _, p := range cfg.GetAppPaths() {
		dirs = append(dirs, paths.LocalBundlesPath(p))
	}
	// Accept a per-remote short "<remote>/<bundle>" name (decision E: a local file
	// of the same spelling still wins); bare/canonical names pass through.
	name := canonicalizeBundleArg(cfg, req.Name, dirs, fs)
	bundle, err := bundles.NewLoader(bundles.NewProjectReader(fs, dirs)).Load(name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	raw, err := afero.ReadFile(fs, bundle.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}
	return &ReadBundleResult{Bundle: bundle, Raw: raw}, nil
}
