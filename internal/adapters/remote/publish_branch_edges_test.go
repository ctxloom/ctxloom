package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	igit "github.com/ctxloom/ctxloom/internal/adapters/git"
)

// fakeGitPublisher is a GitPublisher over a fake git: its clone makes the
// directory and checks out whatever the fake says.
func fakeGitPublisher(t *testing.T, g *igit.Fake) *GitPublisher {
	t.Helper()
	p := &GitPublisher{repoURL: "file:///srv/bundles.git", git: g}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestGitPublisher_OneCloneServesItsOwnBranchOnly: the working clone made for
// the remote's default answers every later request that names no branch, or
// names its branch; a different branch is refused.
func TestGitPublisher_OneCloneServesItsOwnBranchOnly(t *testing.T) {
	main := igit.MustParseBranch("main")
	p := fakeGitPublisher(t, &igit.Fake{CurrentBranchValue: main})
	ctx := context.Background()

	dir, err := p.workTree(ctx, igit.Branch{})
	require.NoError(t, err)
	assert.Equal(t, main, p.branch, "the clone is on the remote's default")

	again, err := p.workTree(ctx, igit.Branch{})
	require.NoError(t, err, "no branch named: the clone's own")
	assert.Equal(t, dir, again)
	same, err := p.workTree(ctx, main)
	require.NoError(t, err)
	assert.Equal(t, dir, same)

	_, err = p.workTree(ctx, igit.MustParseBranch("other"))
	require.ErrorContains(t, err, "one publisher clones one branch")
}

// TestGitPublisher_ADetachedCloneHasNoDefaultBranch: a remote whose HEAD
// leaves the clone detached names no branch to publish to.
func TestGitPublisher_ADetachedCloneHasNoDefaultBranch(t *testing.T) {
	p := fakeGitPublisher(t, &igit.Fake{CurrentBranchDetached: true})
	_, err := p.defaultBranch(context.Background())
	require.ErrorContains(t, err, "detached HEAD")
}

// TestResolvePublishBranch_AFailedLookupIsTheFailure: whichever way the
// default branch is asked — the publisher's own clone, or the forge's API —
// a failure is returned, never an empty branch.
func TestResolvePublishBranch_AFailedLookupIsTheFailure(t *testing.T) {
	cause := errors.New("remote unreachable")
	pm := &PublishManager{}

	_, err := pm.resolvePublishBranch(context.Background(), fakeGitPublisher(t, &igit.Fake{CloneErr: cause}), "file:///srv/bundles.git", "", "", igit.Branch{})
	require.ErrorIs(t, err, cause, "the publisher's own clone")

	mock := newMockGitHubClient()
	mock.repos.GetFunc = func(context.Context, string, string) (*github.Repository, *github.Response, error) {
		return nil, nil, cause
	}
	pm.fetcherFactory = func(string, AuthConfig) (Fetcher, error) { return NewGitHubFetcherWithClient(mock), nil }
	_, err = pm.resolvePublishBranch(context.Background(), NewGitHubPublisherWithClient(newMockGitHubClient()), "https://github.com/o/r", "o", "r", igit.Branch{})
	require.Error(t, err, "the forge's API")
}

// TestGitHubPublisher_AnUnnamedBranchIsRefused: a write or a new branch with
// no branch named is refused before GitHub is asked anything.
func TestGitHubPublisher_AnUnnamedBranchIsRefused(t *testing.T) {
	mock := newMockGitHubClient()
	asked := false
	mock.git.CreateRefFunc = func(context.Context, string, string, *github.Reference) (*github.Reference, *github.Response, error) {
		asked = true
		return &github.Reference{}, nil, nil
	}
	p := NewGitHubPublisherWithClient(mock)

	_, err := p.CreateOrUpdateFiles(context.Background(), "o", "r", igit.Branch{}, "msg", map[string][]byte{"a.txt": []byte("a")})
	require.ErrorContains(t, err, "no branch named")
	require.ErrorContains(t, p.CreateBranch(context.Background(), "o", "r", igit.Branch{}, "sha"), "no branch named")
	assert.False(t, asked)
}

// TestGitHubPublisher_GetFileSHAReadsTheNamedBranch: a named branch is the ref
// read; no branch leaves the ref unset, for the repository's default.
func TestGitHubPublisher_GetFileSHAReadsTheNamedBranch(t *testing.T) {
	var refs []string
	mock := newMockGitHubClient()
	mock.repos.GetContentsFunc = func(_ context.Context, _, _, _ string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
		refs = append(refs, opts.Ref)
		return &github.RepositoryContent{SHA: github.String("sha")}, nil, nil, nil
	}
	p := NewGitHubPublisherWithClient(mock)

	_, err := p.GetFileSHA(context.Background(), "o", "r", "f", igit.MustParseBranch("main"))
	require.NoError(t, err)
	_, err = p.GetFileSHA(context.Background(), "o", "r", "f", igit.Branch{})
	require.NoError(t, err)
	assert.Equal(t, []string{"refs/heads/main", ""}, refs)
}
