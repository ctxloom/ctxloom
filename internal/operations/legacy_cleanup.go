package operations

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// PurgeExtractedBundles removes the on-disk extracted YAML copies of
// remote-pulled bundles left over from before docs/bundle-review-plan.md
// PR 1. The git clone cache + lockfile pair is now the source of truth;
// these files are stale shadows that bundles.Loader would otherwise still
// list and serve (and possibly mask the SHA-pinned seeded copy).
//
// Safety: only files whose YAML has `_source.SHA` set are removed — those
// are unambiguous remote-pull artifacts. Local-authored bundles created
// via CreateBundle never carry `_source`, so they survive untouched.
//
// Returns the number of files removed and any error encountered while
// walking; cleanup is best-effort and a walk error is non-fatal.
// Per-file removal failures are warned but do not abort the walk.
func PurgeExtractedBundles(cfg *config.Config) (int, error) {
	if cfg == nil || len(cfg.GetAppPaths()) == 0 {
		return 0, nil
	}
	bundlesRoot := paths.CacheBundlesPath(cfg.GetAppPaths()[0])
	info, err := os.Stat(bundlesRoot)
	switch {
	// U084-F18: "the directory isn't there" is the ordinary case and a genuine
	// no-op. Every OTHER stat failure — permissions, a broken mount, an I/O
	// error — used to take the same silent `return 0, nil` path, so a cleanup
	// that could not even LOOK reported the same thing as a cleanup with
	// nothing to do. The caller (cli.purgeLegacyBundles) already warns on a
	// non-nil error and continues, so surfacing it costs nothing and stops the
	// silence.
	case errors.Is(err, fs.ErrNotExist):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("cannot inspect legacy bundles cache %s: %w", bundlesRoot, err)
	case !info.IsDir():
		return 0, fmt.Errorf("legacy bundles cache %s is not a directory", bundlesRoot)
	}

	removed := 0
	walkErr := filepath.WalkDir(bundlesRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // Skip unreadable entries.
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".yaml") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		// Legacy remote-pull artifacts embedded a `_source` block; a non-empty
		// SHA there unambiguously marks one. Only that field matters for the
		// keep/remove decision, so probe it directly rather than depend on a
		// shared provenance type.
		var meta struct {
			Source struct {
				SHA string `yaml:"sha"`
			} `yaml:"_source"`
		}
		if yaml.Unmarshal(data, &meta) != nil {
			return nil
		}
		if meta.Source.SHA == "" {
			return nil // Local bundle — leave it alone.
		}
		if rmErr := os.Remove(path); rmErr != nil {
			clidiag.Warn("ctxloom", "failed to remove legacy bundle %s: %v", path, rmErr)
			return nil
		}
		removed++
		return nil
	})
	if walkErr != nil {
		return removed, walkErr
	}

	// Prune now-empty subdirectories so `ls .ctxloom/cache/bundles` stops
	// showing ghosts. Top-level dir stays.
	//
	// U084-F18: WalkDir is PRE-order, so removing in the walk callback only
	// ever cleared the deepest level — a parent holding one empty child is not
	// itself empty when it is visited, so it survived and needed a second run
	// to go. Collect first, then remove DEEPEST-FIRST, so a whole empty chain
	// collapses in one pass regardless of nesting depth.
	var dirs []string
	_ = filepath.WalkDir(bundlesRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || !d.IsDir() || path == bundlesRoot {
			return nil
		}
		dirs = append(dirs, path)
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		entries, derr := os.ReadDir(dir)
		if derr == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}

	return removed, nil
}
