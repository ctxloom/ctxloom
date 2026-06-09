package remote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// resolveContentSHA must route through the constraint resolver: a semver range
// is not a git ref — feeding it to ResolveRef treated "^1.2" as a literal tag
// (go-git even reads a leading "^" as a parent operator), so a constrained
// profile ref that resolved fine through the lock/upgrade path failed plain
// pull.
func TestResolveContentSHA_SemverRange(t *testing.T) {
	mock := NewMockFetcher()
	mock.Tags = []string{"v1.0.0", "v1.2.3", "v2.0.0"}
	mock.Refs = map[string]string{"v1.2.3": "sha-v123", "main": "sha-main"}

	ref, err := ParseReference("https://github.com/o/r@bundles/x@^1.2")
	require.NoError(t, err)

	sha, requested, err := resolveContentSHA(context.Background(), mock, "o", "r", ref)
	require.NoError(t, err)
	require.Equal(t, "sha-v123", sha, "the highest tag satisfying the range, not a literal-ref lookup")
	require.Equal(t, "^1.2", requested, "the constraint itself is what the lock records")
}

// An empty version still tracks the default branch's tip.
func TestResolveContentSHA_DefaultBranch(t *testing.T) {
	mock := NewMockFetcher()
	mock.Refs = map[string]string{"main": "sha-main"}

	ref, err := ParseReference("https://github.com/o/r@bundles/x")
	require.NoError(t, err)

	sha, requested, err := resolveContentSHA(context.Background(), mock, "o", "r", ref)
	require.NoError(t, err)
	require.Equal(t, "sha-main", sha)
	require.Empty(t, requested)
}
