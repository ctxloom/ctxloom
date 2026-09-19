package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GitCloneFetcher carries two ref-resolution ladders. ResolveRef
// tries origin/<ref>, then refs/tags/<ref>, then the bare revision; its doc
// says "This mirrors resolveToCommitHash", which tries origin/<ref>, then the
// bare revision, then refs/tags/<ref>, and then falls back to an explicit
// annotated-tag lookup that ResolveRef does not have. The orders are not the
// same and one ladder has a rung the other lacks, so the doc's claim was
// false whatever else is true.
//
// Per the duplicate-row contract, parity comes FIRST: where two
// implementations of one question disagree, the disagreement is the defect.
// This drives both ladders over one fixture repo carrying every ref shape
// they distinguish -- a remote-tracking branch ahead of its stale local
// branch, a lightweight tag, an annotated tag, a full SHA, an abbreviated
// SHA, a tag and a branch sharing a name, and an absent ref.
func TestGitCloneFetcher_RefLaddersAgree(t *testing.T) {
	fetcher, refs := ladderFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		ref  string
	}{
		{"remote-tracking branch ahead of the stale local branch", "develop"},
		{"lightweight tag", "v1.0.0"},
		{"annotated tag", "v2.0.0"},
		{"full SHA", refs["tip"]},
		{"abbreviated SHA", refs["tip"][:8]},
		{"a tag and a branch sharing one name", "ambiguous"},
		{"a ref that is not there", "no-such-ref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viaResolveRef, refErr := fetcher.ResolveRef(ctx, "owner", "repo", tc.ref)
			viaTreeWalk, walkErr := fetcher.resolveToCommitHash(tc.ref)

			if refErr != nil {
				assert.Error(t, walkErr, "one ladder resolved %q and the other did not", tc.ref)
				return
			}
			require.NoError(t, walkErr, "one ladder resolved %q and the other did not", tc.ref)
			assert.Equal(t, viaResolveRef, walkTip(t, viaTreeWalk),
				"the two ladders must not name different commits for %q", tc.ref)
		})
	}

	t.Run("the remote-tracking rung is load-bearing in BOTH", func(t *testing.T) {
		// The one ordering decision both ladders document: a fetch advances
		// refs/remotes/origin/* but never the local branch, so a branch name
		// must resolve to the fetched tip and not to the stale clone-time
		// commit. Assert the value, not just the agreement -- two ladders can
		// agree on the wrong answer.
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "develop")
		require.NoError(t, err)
		assert.Equal(t, refs["remoteDevelop"], sha)
		assert.NotEqual(t, refs["localDevelop"], sha, "the stale local branch must not win")
	})

	t.Run("a tag that shares a name with a branch resolves to the tag", func(t *testing.T) {
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "ambiguous")
		require.NoError(t, err)
		assert.Equal(t, refs["tagged"], sha,
			"git's own rev-parse rules put refs/tags before refs/heads; neither ladder may invert that")
	})
}

func walkTip(t *testing.T, h plumbing.Hash) string {
	t.Helper()
	return h.String()
}

// ladderFixture builds a repo carrying every ref shape the two ladders
// distinguish, and returns the fetcher plus the commit each ref should name.
func ladderFixture(t *testing.T) (*GitCloneFetcher, map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ladder-repo")
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	commit := func(name string) plumbing.Hash {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(name), 0o644))
		_, err := wt.Add(name + ".txt")
		require.NoError(t, err)
		h, err := wt.Commit(name, &git.CommitOptions{
			Author: &object.Signature{Name: "t", Email: "t@t.test", When: time.Now()},
		})
		require.NoError(t, err)
		return h
	}

	base := commit("base")     // where the stale local branch sits
	tagged := commit("tagged") // what the tags point at
	tip := commit("tip")       // what origin/develop points at

	setRef := func(name string, h plumbing.Hash) {
		require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), h)))
	}
	// A stale local branch and the fetched remote-tracking ref for it.
	setRef("refs/heads/develop", base)
	setRef("refs/remotes/origin/develop", tip)
	// Tags: lightweight, annotated, and one colliding with a branch name.
	setRef("refs/tags/v1.0.0", tagged)
	setRef("refs/heads/ambiguous", base)
	setRef("refs/tags/ambiguous", tagged)
	_, err = repo.CreateTag("v2.0.0", tagged, &git.CreateTagOptions{
		Tagger:  &object.Signature{Name: "t", Email: "t@t.test", When: time.Now()},
		Message: "release two",
	})
	require.NoError(t, err)

	_, err = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{"file://" + dir}})
	require.NoError(t, err)

	fetcher, err := NewGitCloneFetcher(dir, "file://"+dir, ForgeGitGeneric, nil)
	require.NoError(t, err)

	return fetcher, map[string]string{
		"tip":            tip.String(),
		"tagged":         tagged.String(),
		"localDevelop":   base.String(),
		"remoteDevelop":  tip.String(),
		"annotatedPeels": tagged.String(),
	}
}
