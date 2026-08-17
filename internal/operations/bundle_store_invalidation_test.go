package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestBundleStore_SaveIsVisibleToTheNextRead pins the obligation that came with
// sharing one bundle loader per Config.
//
// The loader is memoized for the process's life, so a bundle written through the
// store must drop it or every subsequent read in the same command serves the
// pre-write view: `bundle create` followed by anything that lists, `fragment
// add` followed by an assemble. Before the loader was shared this worked BY
// ACCIDENT — each call built a fresh loader and re-read — so nothing ever had to
// state the requirement, and nothing would report its absence. The failure is
// silent: stale content, exit 0.
//
// The read goes through cfg.BundleLoader(), NOT through the store's own embedded
// loader, because those are different instances and only the Config's is shared.
func TestBundleStore_SaveIsVisibleToTheNextRead(t *testing.T) {
	// A REAL directory, not a memfs: bundleStore builds its filesystem adapter
	// with a nil fs (the OS one) while reads honour the Config's, so a memfs
	// fixture would write somewhere the reader never looks. In production both
	// are the OS filesystem, so this exercises the real path.
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.LocalBundlesPath(appDir), 0o755))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	// Resolve BEFORE the write, so the loader is populated and memoized. Without
	// this the test would pass on a lazily-built loader that never held a stale
	// view in the first place — the assertion has to be about invalidation, not
	// about laziness.
	_, existsBefore := cfg.BundleLoader().Read("late-arrival")
	require.False(t, existsBefore, "sanity: the bundle must not exist before it is written")

	store := bundleStore(cfg, nil)
	require.NoError(t, store.Save(&bundles.Bundle{
		Name:        "late-arrival",
		Version:     "1.0.0",
		Description: "written after the loader was already resolved",
		Path:        filepath.Join(paths.LocalBundlesPath(appDir), "late-arrival.yaml"),
	}))

	_, existsAfter := cfg.BundleLoader().Read("late-arrival")
	require.True(t, existsAfter,
		"a bundle written through the store must be visible to the next read: the Config's bundle "+
			"loader is shared for the life of the process, so a write that does not invalidate it "+
			"leaves every later read in this command serving the pre-write view, with exit 0 and no error")
}

// TestBundleStore_FirstBundleInAFreshProjectIsVisible pins the OTHER half of
// sharing one loader, and this one is easy to miss because invalidation does not
// fix it.
//
// A reader keeps the directories it was constructed with. Invalidation drops the
// memoized READS and never rebuilds the readers, so if the bundles directory did
// not exist when the loader was first built, the project reader holds an empty
// search path FOREVER — and `bundle create` in a fresh project is exactly that
// case. It only worked before because every caller built a new store, which
// re-evaluated the directory list each time.
//
// The fix is that the reader is handed the CONFIGURED directories rather than
// only the ones that happened to exist; localFSReader already skips an absent
// one.
func TestBundleStore_FirstBundleInAFreshProjectIsVisible(t *testing.T) {
	// Deliberately do NOT create the bundles directory: this is a fresh project.
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	// Resolve first, while the bundles dir is still absent — this is what fixes
	// the reader's search path in place.
	_, existsBefore := cfg.BundleLoader().Read("first-ever")
	require.False(t, existsBefore, "sanity: nothing exists in a fresh project")

	require.NoError(t, os.MkdirAll(paths.LocalBundlesPath(appDir), 0o755))
	store := bundleStore(cfg, nil)
	require.NoError(t, store.Save(&bundles.Bundle{
		Name:    "first-ever",
		Version: "1.0.0",
		Path:    filepath.Join(paths.LocalBundlesPath(appDir), "first-ever.yaml"),
	}))

	_, existsAfter := cfg.BundleLoader().Read("first-ever")
	require.True(t, existsAfter,
		"the first bundle created in a project must be visible afterwards: a reader built before the "+
			"bundles directory existed would otherwise hold an empty search path that no invalidation "+
			"can repair, since invalidation drops reads and never rebuilds readers")
}
