package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestListBundles_SelfLoadoutIsNotAnInstalledBundle holds `bundle list` to
// its contract — what the user INSTALLED: local content, pinned remotes, the
// companion loadouts this machine acquired. ctxloom's own loadout is
// intrinsic: nobody installed it and nobody can remove it, so counting it
// under "Installed bundles (N)" states something false and makes `bundle
// remove` name a bundle the user has no way to act on. Another companion's
// loadout stays listed — that one IS something the machine acquired — and
// ctxloom's own content remains addressable by ref (`bundle show`).
func TestListBundles_SelfLoadoutIsNotAnInstalledBundle(t *testing.T) {
	testsupport.Isolate(t)
	appDir, _ := regenTestApp(t)
	cfg := publishedWith(t, gatedFixture(config.Fixture{AppPaths: []string{appDir}}),
		ctxloomOwnLoadout(t),
		bundles.CompanionLoadout{Bin: "ltk", Path: "/fake/ltk", Document: testsupport.RunLoadout("version: 1.0.0\nfragments:\n  ltk:\n    content: LTK\n")},
	)

	infos, err := ListBundles(context.Background(), cfg)
	require.NoError(t, err)
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	assert.Contains(t, names, "ctxloom:companion@ltk", "an acquired companion's loadout is installed content")
	assert.NotContains(t, names, "ctxloom:companion@ctxloom", "ctxloom's own loadout is intrinsic, never installed")
	assert.Len(t, names, 1)

	b, err := cfg.BundleLoader().Load("ctxloom:companion@ctxloom")
	require.NoError(t, err, "the self loadout stays addressable by ref")
	assert.Contains(t, b.MCP, "ctxloom")
}
