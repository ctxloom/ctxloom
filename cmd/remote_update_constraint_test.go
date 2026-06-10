package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
		sha, ok := latestWithinConstraint(ctx, mock, url, "")
		require.True(t, ok)
		require.Equal(t, "mainsha", sha)
	})

	t.Run("branch constraint tracks that branch", func(t *testing.T) {
		sha, ok := latestWithinConstraint(ctx, mock, url, "release")
		require.True(t, ok)
		require.Equal(t, "releasesha", sha)
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
		u, upToDate, err := detectSingleUpdate(ctx, &out, mock, lockfile, "https://github.com/o/r@bundles/x")
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
		_, upToDate, err := detectSingleUpdate(ctx, &out, mock, lf, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.True(t, upToDate, "tip-of-constraint must not report an update even when HEAD moved")
	})

	t.Run("version-suffixed input matches its canonical lock entry", func(t *testing.T) {
		var out strings.Builder
		u, upToDate, err := detectSingleUpdate(ctx, &out, mock, lockfile, "https://github.com/o/r@bundles/x@release")
		require.NoError(t, err)
		require.False(t, upToDate)
		require.Equal(t, "relsha2", u.LatestSHA)
	})

	t.Run("unlocked ref takes its type from the ref, not a bundle default", func(t *testing.T) {
		// A ref with no lock entry must still pull as what its @profiles/ or
		// @bundles/ segment says — pulling a profile as a bundle installs it
		// under the wrong type.
		empty := &remote.Lockfile{}
		var out strings.Builder
		u, upToDate, err := detectSingleUpdate(ctx, &out, mock, empty, "https://github.com/o/r@profiles/p")
		require.NoError(t, err)
		require.False(t, upToDate)
		require.Equal(t, remote.ItemTypeProfile, u.Type, "unlocked profile ref must keep its profile type")

		u, _, err = detectSingleUpdate(ctx, &out, mock, empty, "https://github.com/o/r@bundles/x")
		require.NoError(t, err)
		require.Equal(t, remote.ItemTypeBundle, u.Type)
	})
}
