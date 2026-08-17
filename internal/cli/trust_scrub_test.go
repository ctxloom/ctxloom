package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// scrubProjectRoot builds an isolated project rooted at a tempdir whose default
// profile "dev" pulls in a local bundle "tools" carrying two MCP servers,
// "alpha" and "beta". CTXLOOM_ROOT points config.Load() and projectroot at the
// same root, so the real trust handlers + ApplyHooks operate end-to-end on this
// project. Both servers are project-local executables — first-party under the
// review model, so they are exposed until explicitly rejected. Returns the
// project root (where .claude/.mcp.json lands).
func scrubProjectRoot(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t)        // junk HOME so config.Load reads only this project
	t.Setenv("SSH_AUTH_SOCK", "") // no ssh-agent — trust/blacklist take the unsigned path deterministically
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

	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "tools.yaml"),
		[]byte("version: \"1.0\"\n"+
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

	// Apply the harness once. Both local MCP servers are project-authored, so they
	// auto-trust and are written into settings immediately — no manual trust step.
	_, err = operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	})
	require.NoError(t, err)
	initial := readMCPConfig(t, root)
	require.Contains(t, initial, "alpha-cmd", "an auto-trusted local MCP server is written")
	require.Contains(t, initial, "beta-cmd", "an auto-trusted local MCP server is written")

	// An explicit trust on an already-auto-trusted server is a harmless no-op that
	// still refreshes the managed artifacts; both stay written.
	c, _ := testCmd()
	require.NoError(t, runItemTrust(c, cfg, "tools#mcp/alpha"))
	assert.Contains(t, readMCPConfig(t, root), "alpha-cmd", "the server stays written after an explicit trust")

	c, _ = testCmd()
	require.NoError(t, runItemTrust(c, cfg, "tools#mcp/beta"))
	assert.Contains(t, readMCPConfig(t, root), "beta-cmd", "the sibling stays written too")

	// Blacklisting "alpha" scrubs it from settings on the mutation; the still-
	// trusted sibling "beta" is untouched.
	c, _ = testCmd()
	require.NoError(t, runItemReject(c, cfg, "tools#mcp/alpha"))
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
	scrubProjectRoot(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	// Apply once so a harness exists (harnessApplied → true), guaranteeing the
	// refresh actually reaches ApplyHooks rather than being skipped.
	_, err = operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	})
	require.NoError(t, err)

	// A cancelled context makes the re-apply fail. The mutation must still persist.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := testCmd()
	c.SetContext(ctx)

	require.NoError(t, runItemReject(c, cfg, "tools#mcp/alpha"),
		"a failed managed-artifact refresh must not fail the blacklist")

	alpha := bundles.BundleMCP{Command: "alpha-cmd"}
	alphaPayload, perr := alpha.ContentPayload()
	require.NoError(t, perr)
	ref := trust.Ref{Bundle: "tools", Kind: trust.KindMCP, Name: "alpha", IsLocal: true}
	store := userApprovalsStore(t)
	assert.True(t, store.HasUnsignedRefReject(countersignRefFor(ref)),
		"the rejection must persist even when the post-mutation refresh fails")
	assert.True(t, store.HasUnsignedContentReject(signing.AttestExecMCP, alphaPayload))
}

// TestBlacklist_UnresolvableItemStatesTheRejectionIsRefOnly pins the
// refutation of a finding that claimed `blacklist` reports
// {"status":"rejected"} while having SILENTLY written no content-scoped
// rejection. The ref-only outcome is real and deliberate — spec §5.3 requires
// a rejection to succeed even when the item is already gone, and there are no
// bytes to bind a content-reject to — but it is not silent at any layer, and
// nothing here may become silent. SetBlacklistResult.ContentForms reports the
// forms actually written (empty here), the command warns, and the render says
// which half of the rejection landed.
//
// The "renamed or moved identical copy stays rejected" guarantee is
// content-scoped and therefore inapplicable to an item with no resolvable
// content — which is exactly what the user has to be told, and is.
func TestBlacklist_UnresolvableItemStatesTheRejectionIsRefOnly(t *testing.T) {
	root := scrubProjectRoot(t)
	_ = root

	cfg, err := config.Load()
	require.NoError(t, err)

	warnings := captureWarnings(t)
	c, out := testCmd()
	require.NoError(t, runItemReject(c, cfg, "tools#mcp/ghost"),
		"a rejection must still succeed when the item cannot be resolved — the sticky ref block is the durable guarantee")

	assert.Contains(t, warnings.String(), "no content-reject countersignature was recorded",
		"the missing content-scoped half must be stated, never left for the user to infer from a bare \"rejected\"")
	assert.Contains(t, out.String(), "ref block: recorded",
		"the durable half that DID land must be named")
	assert.Contains(t, out.String(), "content:   not recorded",
		"the half that did not land must be named too")
}
