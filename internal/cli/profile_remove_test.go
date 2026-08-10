package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestRunProfileRemove_BareReportsAndDestroysNothing pins the report side of
// `profile remove`'s safety posture: a bare invocation (profileRemoveYes ==
// false) must say plainly that nothing was removed, name the --yes command
// to apply it, and — the assertion that actually catches a broken guard —
// leave the profile resolvable afterward. A "preview" that quietly removed
// the profile anyway would pass a test that only checks exit code or the
// report text; this one re-resolves the profile.
func TestRunProfileRemove_BareReportsAndDestroysNothing(t *testing.T) {
	cfg := itemFormatProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)
	_, err = operations.CreateProfile(context.Background(), cfg, operations.CreateProfileRequest{Name: "dev", Bundles: []string{"demo"}})
	require.NoError(t, err)

	profileRemoveYes = false
	cmd, buf := testCmd()
	require.NoError(t, runProfileRemove(cmd, []string{"dev"}))
	out := buf.String()
	assert.Contains(t, out, "Nothing was removed")
	assert.Contains(t, out, "--yes")

	_, err = operations.GetProfile(context.Background(), cfg, operations.GetProfileRequest{Name: "dev"})
	assert.NoError(t, err, "the bare (no --yes) path must leave the profile resolvable")
}

// TestRunProfileRemove_YesDestroys pins the apply side: profileRemoveYes ==
// true must actually delete the profile, not just print that it did. Paired
// with the bare-path test above so a regression in either direction — bare
// destroys, or --yes no-ops — is caught by an assertion on the profile's
// continued (non-)existence.
func TestRunProfileRemove_YesDestroys(t *testing.T) {
	cfg := itemFormatProject(t)
	_, err := operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{Name: "demo"})
	require.NoError(t, err)
	_, err = operations.CreateProfile(context.Background(), cfg, operations.CreateProfileRequest{Name: "dev", Bundles: []string{"demo"}})
	require.NoError(t, err)

	profileRemoveYes = true
	t.Cleanup(func() { profileRemoveYes = false })
	cmd, buf := testCmd()
	require.NoError(t, runProfileRemove(cmd, []string{"dev"}))
	assert.Contains(t, buf.String(), `Removed profile "dev"`)

	_, err = operations.GetProfile(context.Background(), cfg, operations.GetProfileRequest{Name: "dev"})
	assert.Error(t, err, "--yes must actually remove the profile")
}
