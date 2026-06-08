package cmd

import (
	"context"
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
