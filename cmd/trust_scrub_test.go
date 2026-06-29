package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// scrubProjectRoot builds an isolated project rooted at a tempdir whose default
// profile "dev" pulls in a local bundle "tools" carrying two MCP servers,
// "alpha" and "beta". CTXLOOM_ROOT points config.Load() and projectroot at the
// same root, so the real trust handlers + ApplyHooks operate end-to-end on this
// project. Both servers are project-local executables, which the cascade never
// auto-trusts (not even at baseline), so each is withheld until explicitly
// trusted. Returns the project root (where .claude/.mcp.json lands).
func scrubProjectRoot(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t) // junk HOME so config.Load reads only this project
	root := t.TempDir()
	t.Setenv(projectroot.EnvVar, root)

	appDir := filepath.Join(root, paths.AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir),
		[]byte("profiles:\n  defaults:\n    - dev\n"), 0o644))

	profilesDir := paths.ProfilesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
		[]byte("name: dev\nbundles:\n  - tools\n"), 0o644))

	bundlesDir := paths.BundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "tools.yaml"),
		[]byte("name: tools\nversion: \"1.0\"\n"+
			"mcp:\n  alpha:\n    command: alpha-cmd\n  beta:\n    command: beta-cmd\n"), 0o644))
	return root
}

// readMCPConfig reads the claude-code .mcp.json at the project root, returning
// "" when it does not exist yet.
func readMCPConfig(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// TestTrustMutations_RefreshManagedArtifacts proves the scrub: a successful
// trust/blacklist mutation refreshes the on-disk managed artifacts immediately
// (no manual `manage hooks install`), so the gate's new decision is reflected in
// the written .mcp.json — a newly-trusted MCP server is (re)written and a
// blacklisted one is scrubbed, while a trusted sibling survives.
func TestTrustMutations_RefreshManagedArtifacts(t *testing.T) {
	root := scrubProjectRoot(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	// Apply the harness once. Both local MCP servers are withheld (local
	// executables are deny-by-default, never baselined), so neither is written
	// yet — but the project now has an applied harness to refresh.
	_, err = operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	})
	require.NoError(t, err)
	initial := readMCPConfig(t, root)
	require.NotContains(t, initial, "alpha-cmd", "a local MCP server is withheld until trusted")
	require.NotContains(t, initial, "beta-cmd", "a local MCP server is withheld until trusted")

	// Trusting each withheld server re-writes it into settings on the mutation,
	// with no manual re-apply.
	c, _ := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "tools#mcp/alpha"))
	assert.Contains(t, readMCPConfig(t, root), "alpha-cmd",
		"trusting a previously-withheld MCP server must (re)write it into settings")

	c, _ = testCmd()
	require.NoError(t, runItemTrust(c, cfg, "tools#mcp/beta"))
	assert.Contains(t, readMCPConfig(t, root), "beta-cmd", "the second trusted server is written too")

	// Blacklisting "alpha" scrubs it from settings on the mutation; the still-
	// trusted sibling "beta" is untouched.
	c, _ = testCmd()
	require.NoError(t, runBlacklist(c, cfg, "tools#mcp/alpha"))
	after := readMCPConfig(t, root)
	assert.NotContains(t, after, "alpha-cmd",
		"blacklisting must scrub the now-withheld MCP server from settings")
	assert.Contains(t, after, "beta-cmd", "the trusted sibling MCP server survives the scrub")
}

// TestTrustMutations_RefreshFailureDoesNotBlock proves fault tolerance: when the
// managed-artifact re-apply fails (here a cancelled context aborts ApplyHooks),
// the trust mutation still persists and the command does not error — the trust
// change already landed, so a refresh failure is only a warning.
func TestTrustMutations_RefreshFailureDoesNotBlock(t *testing.T) {
	root := scrubProjectRoot(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	// Apply once so a harness exists (harnessApplied → true), guaranteeing the
	// refresh actually reaches ApplyHooks rather than being skipped.
	_, err = operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	})
	require.NoError(t, err)

	// A cancelled context makes the re-apply fail. The mutation must still persist.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := testCmd()
	c.SetContext(ctx)

	require.NoError(t, runBlacklist(c, cfg, "tools#mcp/alpha"),
		"a failed managed-artifact refresh must not fail the blacklist")

	store := loadTrustStore(t, filepath.Join(root, paths.AppDirName))
	assert.True(t, store.BlacklistMatch(remote.LocalSource, "tools#mcp/alpha"),
		"the blacklist must persist even when the post-mutation refresh fails")
}
