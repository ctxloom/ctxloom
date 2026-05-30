package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// commitTo adds a file and commits it to the non-bare repo at dir, returning
// the new commit SHA.
func commitTo(t *testing.T, dir, name, content string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(name)
	require.NoError(t, err)
	h, err := wt.Commit("commit "+name, &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Now()},
	})
	require.NoError(t, err)
	return h.String()
}

// originDefaultRev returns the remote-tracking ref for the clone's default
// branch. createTestRepo commits on whatever the local default branch is
// (master or main depending on git config), so resolve via origin/HEAD's
// target rather than hardcoding a branch name.
func originDefaultRev(t *testing.T, cloneDir string) string {
	t.Helper()
	repo, err := git.PlainOpen(cloneDir)
	require.NoError(t, err)
	// Prefer the explicit remote HEAD symref; fall back to main/master.
	if ref, err := repo.Reference(plumbing.NewRemoteHEADReferenceName("origin"), true); err == nil {
		return ref.Hash().String()
	}
	for _, b := range []string{"main", "master"} {
		if h, err := repo.ResolveRevision(plumbing.Revision("refs/remotes/origin/" + b)); err == nil {
			return h.String()
		}
	}
	t.Fatalf("no origin default branch ref found in %s", cloneDir)
	return ""
}

// TestRepoCache_UpdateRepo_AdvancesOrigin is the regression test for the
// shallow-refetch bug: after a shallow clone, advancing the remote and running
// UpdateRepo must move the remote-tracking default branch to the live remote
// HEAD. The old Depth:1 refetch left origin/* pinned at the original shallow
// boundary, so `remote update` never saw new commits.
//
// createTestRepo (in repo_cache_test.go) builds a non-bare git repo with an
// initial commit that we clone from over a file:// URL — no network.
func TestRepoCache_UpdateRepo_AdvancesOrigin(t *testing.T) {
	tmpDir := t.TempDir()
	sourceRepo := createTestRepo(t, tmpDir)
	repoURL := "file://" + sourceRepo

	cacheDir := filepath.Join(tmpDir, "cache")
	cache := NewRepoCache(cacheDir, AuthConfig{})

	// Shallow clone at the initial commit (empty ref → default branch only).
	cloneDir, err := cache.EnsureRepo(context.Background(), repoURL, ForgeGitHub)
	require.NoError(t, err)
	initialSHA := originDefaultRev(t, cloneDir)

	// Advance the remote with a new commit on its default branch.
	liveSHA := commitTo(t, sourceRepo, "second.txt", "two")
	require.NotEqual(t, initialSHA, liveSHA, "precondition: remote HEAD did not advance")

	// Update must advance the cached remote-tracking default branch to live HEAD.
	_, err = cache.UpdateRepo(context.Background(), repoURL, ForgeGitHub)
	require.NoError(t, err)

	got := originDefaultRev(t, cloneDir)
	require.Equalf(t, liveSHA, got,
		"origin default branch not advanced: got %s, want live %s (initial was %s)", got, liveSHA, initialSHA)
}
