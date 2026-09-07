package operations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// TestRemoteFromCachePath_ResolvesUnderEveryLayout pins step 1 of
// resolveRemoteForPath's inference chain against the LAYOUTED cache tree.
//
// The paths here are spelled as LITERALS on purpose. Building them from
// paths.CacheBundlesPathFor would make this test agree with whatever the
// accessor currently returns, so it would pass under any layout and catch
// nothing — and a layout move is the exact regression it exists to catch.
//
// The v2 case is the regression pin: an install lands under the layout subtree,
// so relative to the BARE cache/bundles root the first segment is "v2", which is
// never a registered remote. Rooting at the bare path therefore failed to
// resolve a remote for every bundle the current code installs.
func TestRemoteFromCachePath_ResolvesUnderEveryLayout(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{app}})

	reg, err := remote.NewRegistry(filepath.Join(app, "remotes.yaml"))
	require.NoError(t, err)
	require.NoError(t, reg.Add("personal", "https://github.com/ben/ctxloom-personal"))

	cacheBundles := filepath.Join(app, "cache", "bundles")

	for _, layout := range []string{"v2", "v1"} {
		t.Run("a bundle under "+layout+" resolves its remote", func(t *testing.T) {
			p := filepath.Join(cacheBundles, layout, "personal", "mybundle", "bundle.yaml")
			name, ok := remoteFromCachePath(cfg, reg, p)
			require.True(t, ok, "a cached bundle under %s must resolve to the remote that installed it", layout)
			assert.Equal(t, "personal", name)
		})
	}

	t.Run("an unregistered first segment does not resolve", func(t *testing.T) {
		p := filepath.Join(cacheBundles, "v2", "stranger", "mybundle", "bundle.yaml")
		_, ok := remoteFromCachePath(cfg, reg, p)
		assert.False(t, ok, "only a REGISTERED remote may be inferred from a path")
	})

	t.Run("the layout segment itself is never mistaken for a remote", func(t *testing.T) {
		// Directly under the bare root, so the first segment is the layout.
		p := filepath.Join(cacheBundles, "v2", "bundle.yaml")
		_, ok := remoteFromCachePath(cfg, reg, p)
		assert.False(t, ok, "a layout segment is not a remote name")
	})

	t.Run("a path outside the cache does not resolve", func(t *testing.T) {
		p := filepath.Join(app, "content", "bundles", "v2", "personal", "bundle.yaml")
		_, ok := remoteFromCachePath(cfg, reg, p)
		assert.False(t, ok, "content/ encodes no remote and must not be inferred from")
	})
}
