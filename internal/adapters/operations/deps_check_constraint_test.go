package operations

import (
	"context"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// latestWithinConstraint makes `update` constraint-aware: a constraint-less entry
// tracks the default branch, a branch constraint tracks that branch's tip, and an
// exact pin resolves to itself. (Semver-range resolution needs real tags and is
// covered in internal/adapters/remote.)
func TestLatestWithinConstraint(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.DefaultBranch = "main"
	mock.Refs = map[string]string{
		"main":    "mainsha",
		"release": "releasesha",
	}
	ctx := context.Background()
	const url = "https://github.com/o/r"

	t.Run("empty constraint tracks default branch", func(t *testing.T) {
		sha, ok, err := latestWithinConstraint(ctx, mock, url, "")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "mainsha", sha)
	})

	t.Run("branch constraint tracks that branch", func(t *testing.T) {
		sha, ok, err := latestWithinConstraint(ctx, mock, url, "release")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "releasesha", sha)
	})

	// A malformed URL is a genuine resolution FAILURE, not "nothing
	// newer found" — before the fix these collapsed to the same ok=false with
	// no way for a caller to tell them apart.
	t.Run("malformed URL is a resolution error, not a false ok", func(t *testing.T) {
		sha, ok, err := latestWithinConstraint(ctx, mock, "not-a-valid-repo-url", "")
		require.Error(t, err)
		require.False(t, ok)
		require.Empty(t, sha)
	})
}

// detectSingleUpdate must honor the locked entry's version constraint exactly
// like the batch path (latestWithinConstraint): a branch-constrained entry
// tracks that branch's tip, never default-branch HEAD — HEAD can exceed what
// the manifest asked for. RequestedVersion rides along so apply can pin the
// SHA without freezing the constraint.
func TestDetectSingleUpdate_HonorsConstraint(t *testing.T) {
	mock := remote.NewMockFetcher()
	mock.DefaultBranch = "main"
	mock.Refs = map[string]string{
		"main":    "mainsha",
		"release": "relsha2",
	}
	ctx := context.Background()

	lockfile := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{
		"ctxloom+git://github.com/o/r//bundles/x": {SHA: "relsha1", RequestedVersion: "release"},
	}}

	t.Run("constrained entry tracks its branch, not HEAD", func(t *testing.T) {
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x")
		require.NoError(t, rerr)
		s, err := detectSingleUpdate(ctx, mock, lockfile, ref, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.False(t, s.UpToDate())
		require.Equal(t, "relsha2", s.LatestSHA, "must resolve within the constraint, not default-branch HEAD")
		require.Equal(t, "release", s.Update.RequestedVersion)
		require.Equal(t, remote.ItemTypeBundle, s.Update.Type)
	})

	t.Run("entry at the constraint tip is up to date", func(t *testing.T) {
		lf := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{
			"ctxloom+git://github.com/o/r//bundles/x": {SHA: "relsha2", RequestedVersion: "release"},
		}}
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x")
		require.NoError(t, rerr)
		s, err := detectSingleUpdate(ctx, mock, lf, ref, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.True(t, s.UpToDate(), "tip-of-constraint must not report an update even when HEAD moved")
	})

	t.Run("version-suffixed input matches its canonical lock entry", func(t *testing.T) {
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x@release")
		require.NoError(t, rerr)
		s, err := detectSingleUpdate(ctx, mock, lockfile, ref, "https://github.com/o/r@bundles/x@release")
		require.NoError(t, err)
		require.False(t, s.UpToDate())
		require.Equal(t, "relsha2", s.LatestSHA)
	})

	t.Run("unlocked ref is treated as a bundle", func(t *testing.T) {
		// Top-level profile distribution was retired, so a ref with no lock entry
		// pulls as a bundle — the only distributed item type.
		empty := &remote.Lockfile{}
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x")
		require.NoError(t, rerr)
		s, err := detectSingleUpdate(ctx, mock, empty, ref, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.False(t, s.UpToDate(), "an unlocked reference is never current")
		require.Equal(t, remote.ItemTypeBundle, s.Update.Type)
	})
}

// TestDetectUpdates_FailedChecksAreCounted pins a fix: an entry whose
// reference could not even be parsed used to `continue` with ZERO
// diagnostic and zero effect on any counter — indistinguishable from an
// entry that was checked and found current. If every entry in a lockfile
// hit this, `checkAll` printed "All items are up to date!" having actually
// checked nothing. detectUpdates returns it as a typed Unchecked row so the
// caller can tell "verified current" apart from "could not be checked".
func TestDetectUpdates_FailedChecksAreCounted(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})
	lockfile := &remote.Lockfile{Bundles: map[trust.BundleKey]remote.LockEntry{
		// Fails at remote.ParseReference — not a recognized scheme at all.
		"::::not-a-valid-reference": {SHA: "somesha", RequestedVersion: "main"},
	}}

	updates, unchecked, skipped := detectUpdates(context.Background(), cfg, remote.AuthConfig{}, lockfile)
	assert.Empty(t, updates)
	assert.Equal(t, 0, skipped)
	require.Len(t, unchecked, 1, "a reference that fails to parse must be reported unchecked, not silently dropped")
	assert.Equal(t, UncheckedUnparseable, unchecked[0].Reason)
}
