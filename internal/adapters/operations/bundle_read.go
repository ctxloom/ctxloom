package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// ReadBundleRequest is the input for ReadBundle.
type ReadBundleRequest struct {
	Name string `json:"name"`

	// FS is an optional filesystem (defaults to the OS filesystem).
	FS afero.Fs `json:"-"`
}

// ReadBundleResult carries the loaded bundle and its stored bytes.
type ReadBundleResult struct {
	Bundle *bundles.Bundle `json:"-"`
	// Raw is the bundle as stored: each file of its tree, the envelope first
	// and the rest in path order, under a "==> <path> <==" header. The
	// tree's attestation (SHA256SUMS, .sigs/) is left out — it is about the
	// content, not content.
	Raw []byte `json:"-"`
}

// ReadBundle loads a bundle by name and returns it along with its stored
// files — the read half behind `bundle view`. The frontend renders; the file
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
	bundle, err := bundles.NewLoader(projectReader(fs, dirs)).Load(name)
	if err != nil {
		return nil, fmt.Errorf("bundle %q not found: %w", req.Name, err)
	}
	raw, err := dumpBundleTree(fs, filepath.Dir(bundle.Path))
	if err != nil {
		return nil, fmt.Errorf("failed to read bundle: %w", err)
	}
	return &ReadBundleResult{Bundle: bundle, Raw: raw}, nil
}

func dumpBundleTree(fs afero.Fs, dir string) ([]byte, error) {
	var rels []string
	err := afero.Walk(fs, dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if rel == content.SigDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if rel != content.ManifestPath && rel != bundles.DirectoryFormManifest {
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	var buf bytes.Buffer
	for i, rel := range append([]string{bundles.DirectoryFormManifest}, rels...) {
		data, err := afero.ReadFile(fs, filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if i > 0 {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "==> %s <==\n", rel)
		buf.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}
