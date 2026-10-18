package config

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

const (
	// One bundle, two spellings: the canonical ctxloom URI a profile writes and
	// the legacy fetch address an inherited profile resolves a bare name to.
	spellingCanonical = "ctxloom+git://example.test/repo//bundles/sequential-thinking"
	spellingLegacy    = "https://example.test/repo@bundles/sequential-thinking"
)

// remoteSeqThinkingCatalog binds cfg to a catalog holding ONE remote bundle,
// read the way a pinned lockfile entry is.
func remoteSeqThinkingCatalog(t *testing.T, cfg *Config) {
	t.Helper()
	const root = "/pinned"
	fsys := afero.NewMemMapFs()
	bundletree.WriteBundle(t, fsys, root, "sequential-thinking", &bundles.Bundle{
		Version: "1.0",
		MCP: map[string]bundles.BundleMCP{
			"sequential-thinking": {Command: "npx", Args: []string{"-y", "server"}},
		},
	})
	tree, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	cat := bundles.Resolve(context.Background(), cfg.rep.Sink,
		bundles.NewProjectReader(cfg.getFS(), cfg.BundleReaderDirs()),
		bundles.NewRepoFSReader(tree, spellingLegacy, bundles.WithRepoURL("https://example.test/repo")))
	cfg.bindCatalog(func() bundles.Catalog { return cat })
}

// TestResolveBundleMCPServers_OneBundleTwoSpellings_IsNotAContest: two profiles
// naming the SAME bundle, one in each spelling, are one source. Comparing the
// raw ref text made the bundle contest itself — a fatal finding whose only
// remedy ("rename the server") has nothing to act on.
func TestResolveBundleMCPServers_OneBundleTwoSpellings_IsNotAContest(t *testing.T) {
	resetStrictness(t)

	cfg := mcpContestFixture(t, nil,
		map[string]string{"alpha": spellingCanonical, "beta": spellingLegacy})
	remoteSeqThinkingCatalog(t, cfg)

	mark := strictness.Checkpoint()
	result := cfg.ResolveBundleMCPServers([]string{"alpha", "beta"})

	require.Contains(t, result, "sequential-thinking", "the bundle's server is delivered")
	assert.Equal(t, "npx", result["sequential-thinking"].Command)
	assert.Len(t, result, 1, "delivered exactly once")
	assert.Empty(t, strictness.Since(mark), "one bundle spelled two ways is not a name contest")
}

// TestResolveBundleMCPServers_ContestIsReportedOncePerProcess: a genuine
// contest between two DIFFERENT bundles is one finding however many times the
// resolve runs (assembly, hooks and doctor each resolve).
func TestResolveBundleMCPServers_ContestIsReportedOncePerProcess(t *testing.T) {
	resetStrictness(t)

	cfg := mcpContestFixture(t,
		map[string]string{
			"bundle-a": mcpBundleYAML([2]string{"shared-server", "alpha-cmd"}),
			"bundle-b": mcpBundleYAML([2]string{"shared-server", "beta-cmd"}),
		},
		map[string]string{"alpha": "bundle-a", "beta": "bundle-b"},
	)

	mark := strictness.Checkpoint()
	for range 3 {
		result := cfg.ResolveBundleMCPServers([]string{"alpha", "beta"})
		assert.Equal(t, "alpha-cmd", result["shared-server"].Command)
	}

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "one contest is one finding across resolution passes")
	assert.Equal(t, report.KindBundle, findings[0].Kind)
}
