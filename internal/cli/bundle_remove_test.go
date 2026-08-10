package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestRunBundleRemove_BareReportsAndDestroysNothing pins the report side of
// `bundle remove`'s safety posture: a bare invocation (bundleRemoveYes ==
// false) must name what it would destroy, say plainly that nothing was
// removed, name the --yes command to apply it, and — the assertion that
// actually catches a broken guard — leave the bundle file on disk. A
// "preview" that quietly deletes anyway would pass a test that only checks
// exit code or the report text; this one re-reads the filesystem.
func TestRunBundleRemove_BareReportsAndDestroysNothing(t *testing.T) {
	cfg := itemFormatProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)

	bundleRemoveYes = false
	cmd, buf := testCmd()
	require.NoError(t, runBundleRemove(cmd, []string{"demo"}))
	out := buf.String()
	assert.Contains(t, out, "Nothing was removed")
	assert.Contains(t, out, "--yes")

	_, err = operations.GetBundle(cfg, "demo")
	assert.NoError(t, err, "the bare (no --yes) path must leave the bundle on disk")
}

// TestRunBundleRemove_YesDestroys pins the apply side: bundleRemoveYes ==
// true must actually delete the bundle file, not just print that it did.
// Paired with the bare-path test above so a regression in either direction —
// bare destroys, or --yes no-ops — is caught by an assertion on the
// bundle's continued (non-)existence, not just on exit code or a status
// string a broken implementation could still print.
func TestRunBundleRemove_YesDestroys(t *testing.T) {
	cfg := itemFormatProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)

	bundleRemoveYes = true
	t.Cleanup(func() { bundleRemoveYes = false })
	cmd, buf := testCmd()
	require.NoError(t, runBundleRemove(cmd, []string{"demo"}))
	assert.Contains(t, buf.String(), "Removed bundle")

	_, err = operations.GetBundle(cfg, "demo")
	assert.Error(t, err, "--yes must actually remove the bundle from disk")
}
