package remote

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tagHead creates a lightweight tag at the repo's current HEAD.
func tagHead(t *testing.T, dir, name string) {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	_, err = repo.CreateTag(name, head.Hash(), nil)
	require.NoError(t, err)
}

// buildTaggedRepo builds a repo with four tagged releases plus a non-semver tag,
// and returns the fetcher over it. Each release is its own commit so tags point
// at distinct SHAs.
func buildTaggedRepo(t *testing.T) *GitCloneFetcher {
	t.Helper()
	dir := newTestRepo(t)
	writeCommit(t, dir, "v1.0.0", map[string]string{"a.txt": "1.0.0"}, nil)
	tagHead(t, dir, "v1.0.0")
	writeCommit(t, dir, "v1.2.0", map[string]string{"a.txt": "1.2.0"}, nil)
	tagHead(t, dir, "v1.2.0")
	writeCommit(t, dir, "v1.3.0", map[string]string{"a.txt": "1.3.0"}, nil)
	tagHead(t, dir, "v1.3.0")
	tagHead(t, dir, "nightly") // non-semver tag at the same commit
	writeCommit(t, dir, "v2.0.0", map[string]string{"a.txt": "2.0.0"}, nil)
	tagHead(t, dir, "v2.0.0")

	f, err := NewGitCloneFetcher(dir, "https://github.com/o/r", ForgeGitHub, nil)
	require.NoError(t, err)
	return f
}

func TestGitCloneFetcher_ListTags(t *testing.T) {
	f := buildTaggedRepo(t)
	tags, err := f.ListTags(context.Background(), "o", "r")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"v1.0.0", "v1.2.0", "v1.3.0", "nightly", "v2.0.0"}, tags)
}

func TestFetcherRepoVersions_ResolveSemverRange(t *testing.T) {
	f := buildTaggedRepo(t)
	rv := NewFetcherRepoVersions(f, "o", "r")
	ctx := context.Background()

	// ^1.2 → highest 1.x at or above 1.2, excluding 2.0.0 → v1.3.0.
	got, err := ResolveConstraint(ctx, "^1.2", rv)
	require.NoError(t, err)
	assert.Equal(t, "v1.3.0", got.Version)

	// The resolved SHA must equal resolving the chosen tag directly.
	wantSHA, err := f.ResolveRef(ctx, "o", "r", "v1.3.0")
	require.NoError(t, err)
	assert.Equal(t, wantSHA, got.SHA)
}

func TestFetcherRepoVersions_ResolveExactAndDefault(t *testing.T) {
	f := buildTaggedRepo(t)
	rv := NewFetcherRepoVersions(f, "o", "r")
	ctx := context.Background()

	// Exact semver pins the matching tag.
	exact, err := ResolveConstraint(ctx, "v1.2.0", rv)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", exact.Version)

	// Empty constraint tracks the default branch tip; Version stays empty.
	def, err := ResolveConstraint(ctx, "", rv)
	require.NoError(t, err)
	assert.Empty(t, def.Version)
	assert.NotEmpty(t, def.SHA)
}
