package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// builtinRefsIn reports the builtin reads this config's loader actually
// produced. A test asserting a builtin is ABSENT from a listing is worthless
// unless something proves it was present to begin with — absence satisfying
// absence is this suite's documented false-green shape — so every assertion
// below is guarded by this returning a non-empty set.
func builtinRefsIn(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	var refs []string
	for _, read := range cfg.BundleLoader().Catalog().Reads() {
		if strings.HasPrefix(read.Ref(), trust.BuiltinSourcePrefix) {
			refs = append(refs, read.Ref())
		}
	}
	require.NotEmpty(t, refs,
		"guard: this config's loader must actually read builtins, or the assertions below prove nothing")
	return refs
}

func listedNames(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	infos, err := ListBundles(context.Background(), cfg)
	require.NoError(t, err)
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

// TestListBundles_ExcludesBuiltinsAndKeepsProjectBundles holds `bundle list` to
// the contract its own help text states: local bundles under
// .ctxloom/content/bundles plus the remotes pinned in the lockfile. A builtin
// ships INSIDE the binary, was never installed, and cannot be removed — counting
// it under "Installed bundles (N)" states something false, and `bundle remove`
// then names a bundle the user has no way to act on.
func TestListBundles_ExcludesBuiltinsAndKeepsProjectBundles(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)
	builtins := builtinRefsIn(t, cfg)

	listed := listedNames(t, cfg)

	for _, ref := range builtins {
		assert.NotContains(t, listed, ref, "a builtin is not installed and must not be listed")
	}
	assert.Contains(t, listed, "demo", "excluding builtins must not drop the project's own bundles")
}

// TestListBundles_ExcludesBuiltinsAndKeepsPinnedRemotes is the other side of the
// same filter, and the reason it is scoped rather than narrowed to the project:
// a lockfile-pinned remote WAS installed on this machine and must keep listing.
// Narrowing the exclusion to "project only" passes the test above and silently
// empties the listing for every user with a dependency; this fails on it.
func TestListBundles_ExcludesBuiltinsAndKeepsPinnedRemotes(t *testing.T) {
	cfg, _, bundleRef := seedRemoteFixture(t)
	builtins := builtinRefsIn(t, cfg)

	listed := listedNames(t, cfg)

	for _, ref := range builtins {
		assert.NotContains(t, listed, ref, "a builtin is not installed and must not be listed")
	}
	assert.Contains(t, listed, bundleRef, "a lockfile-pinned remote bundle must still be listed")
}
