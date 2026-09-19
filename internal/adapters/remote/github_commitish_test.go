package remote

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `if len(ref) < 7 || len(ref) > 40` was the whole of "looks like a
// commit SHA". Two unexplained numbers, and a test that does not answer the
// question they are standing in for -- a commit SHA is hexadecimal, and length
// alone admits every ordinary branch or tag name of seven characters or more.
//
// So `develop` was sent to the commits endpoint before anyone asked whether it
// was a branch. The answer usually came back right, because GitHub's commits
// endpoint resolves branches and tags too, which is precisely what made the
// mismatch survive: the guard's stated meaning and its actual test disagreed
// without producing a wrong answer, only a needless round trip and a ladder
// whose first rung fires for inputs it was never written for.
func TestGitHubFetcher_ResolveRef_CommitProbeIsForCommitish(t *testing.T) {
	ctx := context.Background()

	newMock := func(t *testing.T, commitProbed *bool) *mockGitHubClient {
		t.Helper()
		mock := newMockGitHubClient()
		mock.repos.GetCommitFunc = func(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
			*commitProbed = true
			return &github.RepositoryCommit{SHA: github.String("commit-endpoint-sha")}, nil, nil
		}
		mock.repos.GetBranchFunc = func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
			return &github.Branch{Commit: &github.RepositoryCommit{SHA: github.String("branch-sha")}}, nil, nil
		}
		return mock
	}

	for _, ref := range []string{"develop", "release-branch", "feature/long-lived-topic"} {
		t.Run("a branch name is not probed as a commit: "+ref, func(t *testing.T) {
			probed := false
			fetcher := NewGitHubFetcherWithClient(newMock(t, &probed))

			sha, err := fetcher.ResolveRef(ctx, "owner", "repo", ref)
			require.NoError(t, err)
			assert.False(t, probed, "%q is not hexadecimal and cannot be a commit SHA", ref)
			assert.Equal(t, "branch-sha", sha)
		})
	}

	for _, ref := range []string{"abc1234", "ABC1234", "0123456789abcdef0123456789abcdef01234567"} {
		t.Run("a commit-ish ref still takes the commit rung first: "+ref, func(t *testing.T) {
			probed := false
			fetcher := NewGitHubFetcherWithClient(newMock(t, &probed))

			sha, err := fetcher.ResolveRef(ctx, "owner", "repo", ref)
			require.NoError(t, err)
			assert.True(t, probed)
			assert.Equal(t, "commit-endpoint-sha", sha)
		})
	}

	t.Run("hex but too short or too long is not commit-ish", func(t *testing.T) {
		for _, ref := range []string{"abc12", "abc123", "0123456789abcdef0123456789abcdef012345678"} {
			probed := false
			fetcher := NewGitHubFetcherWithClient(newMock(t, &probed))
			_, err := fetcher.ResolveRef(ctx, "owner", "repo", ref)
			require.NoError(t, err)
			assert.False(t, probed, "%q is outside the abbreviation bounds a forge will resolve", ref)
		}
	})

	t.Run("dropping the probe does not lose a tag", func(t *testing.T) {
		// The rung that no longer fires for a non-hex ref has to be covered by
		// the rungs below it, or this trades a wasted call for a lost answer.
		mock := newMockGitHubClient()
		mock.repos.GetCommitFunc = func(ctx context.Context, owner, repo, sha string, opts *github.ListOptions) (*github.RepositoryCommit, *github.Response, error) {
			t.Fatal("a tag name is not commit-ish and must not reach the commits endpoint")
			return nil, nil, nil
		}
		mock.repos.GetBranchFunc = func(ctx context.Context, owner, repo, branch string, maxRedirects int) (*github.Branch, *github.Response, error) {
			return nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}
		mock.git.GetRefFunc = func(ctx context.Context, owner, repo, ref string) (*github.Reference, *github.Response, error) {
			return &github.Reference{Object: &github.GitObject{Type: github.String("commit"), SHA: github.String("tag-sha")}}, nil, nil
		}

		fetcher := NewGitHubFetcherWithClient(mock)
		sha, err := fetcher.ResolveRef(ctx, "owner", "repo", "v1.2.3-rc1")
		require.NoError(t, err)
		assert.Equal(t, "tag-sha", sha)
	})
}
