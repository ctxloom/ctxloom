package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// latestWithinConstraint makes `update` constraint-aware: a constraint-less entry
// tracks the default branch, a branch constraint tracks that branch's tip, and an
// exact pin resolves to itself. (Semver-range resolution needs real tags and is
// covered in internal/remote.)
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

	lockfile := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
		"https://github.com/o/r@bundles/x": {SHA: "relsha1", RequestedVersion: "release"},
	}}

	t.Run("constrained entry tracks its branch, not HEAD", func(t *testing.T) {
		var out strings.Builder
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x")
		require.NoError(t, rerr)
		u, upToDate, err := detectSingleUpdate(ctx, &out, mock, lockfile, ref, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.False(t, upToDate)
		require.Equal(t, "relsha2", u.LatestSHA, "must resolve within the constraint, not default-branch HEAD")
		require.Equal(t, "release", u.RequestedVersion)
		require.Equal(t, remote.ItemTypeBundle, u.Type)
	})

	t.Run("entry at the constraint tip is up to date", func(t *testing.T) {
		lf := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
			"https://github.com/o/r@bundles/x": {SHA: "relsha2", RequestedVersion: "release"},
		}}
		var out strings.Builder
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x")
		require.NoError(t, rerr)
		_, upToDate, err := detectSingleUpdate(ctx, &out, mock, lf, ref, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.True(t, upToDate, "tip-of-constraint must not report an update even when HEAD moved")
	})

	t.Run("version-suffixed input matches its canonical lock entry", func(t *testing.T) {
		var out strings.Builder
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x@release")
		require.NoError(t, rerr)
		u, upToDate, err := detectSingleUpdate(ctx, &out, mock, lockfile, ref, "https://github.com/o/r@bundles/x@release")
		require.NoError(t, err)
		require.False(t, upToDate)
		require.Equal(t, "relsha2", u.LatestSHA)
	})

	t.Run("unlocked ref is treated as a bundle", func(t *testing.T) {
		// Top-level profile distribution was retired, so a ref with no lock entry
		// pulls as a bundle — the only distributed item type.
		empty := &remote.Lockfile{}
		var out strings.Builder
		ref, rerr := parseCheckRef("https://github.com/o/r@bundles/x")
		require.NoError(t, rerr)
		u, upToDate, err := detectSingleUpdate(ctx, &out, mock, empty, ref, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.False(t, upToDate)
		require.Equal(t, remote.ItemTypeBundle, u.Type)
	})
}

// TestDetectUpdates_FailedChecksAreCounted pins a fix: an entry whose
// reference could not even be parsed used to `continue` with ZERO
// diagnostic and zero effect on any counter — indistinguishable from an
// entry that was checked and found current. If every entry in a lockfile
// hit this, `checkAll` printed "All items are up to date!" having actually
// checked nothing. detectUpdates now returns a third count so the caller can
// tell "verified current" apart from "could not be checked".
func TestDetectUpdates_FailedChecksAreCounted(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})
	lockfile := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
		// Fails at remote.ParseReference — not a recognized scheme at all.
		"::::not-a-valid-reference": {SHA: "somesha", RequestedVersion: "main"},
	}}

	var out strings.Builder
	updates, skipped, failed := detectUpdates(context.Background(), &out, cfg, remote.AuthConfig{}, lockfile)
	assert.Empty(t, updates)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, 1, failed, "a reference that fails to parse must be counted as a failed check, not silently dropped")
}
