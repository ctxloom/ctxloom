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

// CreateOrUpdateFile discarded GetFileSHA's error. GetFileSHA is
// careful to separate the two answers -- a 404 comes back as ("", nil), "the
// file is not there", and every other failure comes back as an error, "I could
// not find out" -- and the blank identifier collapsed them into each other.
//
// The empty SHA that results is what decides the shape of the request: with no
// SHA, the contents API is asked to CREATE the path. So a transient read
// failure over an existing file makes this method emit a create for something
// it is actually meant to update, on the publish path that writes a bundle and
// its detached signature.
//
// This is the same defect closed one layer up in preparePublish; the
// last gate before the network write still had it.
func TestGitHubPublisher_CreateOrUpdateFile_ExistenceCheckFailureAborts(t *testing.T) {
	ctx := context.Background()

	t.Run("a transient existence-check failure aborts before any write", func(t *testing.T) {
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusInternalServerError}}, errors.New("500 Internal Server Error")
		}
		wrote := false
		mock.repos.CreateFileFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error) {
			wrote = true
			return &github.RepositoryContentResponse{Commit: github.Commit{SHA: github.String("should-not-happen")}}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		sha, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "bundle.yaml", "main", "publish", []byte("body"))

		require.Error(t, err, `"could not check" must not be read as "not there"`)
		assert.Empty(t, sha, "no SHA may be reported for a write that did not happen")
		assert.False(t, wrote, "the forge must not be asked to create a path we failed to look up")
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("a path that is a directory aborts too", func(t *testing.T) {
		// GetFileSHA's other non-404 refusal. It cannot yield a file SHA, so
		// proceeding would again emit a create.
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, []*github.RepositoryContent{{Name: github.String("inner.yaml")}}, nil, nil
		}
		wrote := false
		mock.repos.CreateFileFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error) {
			wrote = true
			return nil, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		_, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "somedir", "main", "publish", []byte("body"))
		require.Error(t, err)
		assert.False(t, wrote)
	})

	t.Run("a genuine 404 still creates", func(t *testing.T) {
		// The distinction has to keep cutting the other way: absence is a
		// legitimate answer and must still produce a create, or first-publish
		// stops working.
		mock := newMockGitHubClient()
		mock.repos.GetContentsFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
			return nil, nil, &github.Response{Response: &http.Response{StatusCode: http.StatusNotFound}}, errors.New("not found")
		}
		var sentSHA *string
		mock.repos.CreateFileFunc = func(ctx context.Context, owner, repo, path string, opts *github.RepositoryContentFileOptions) (*github.RepositoryContentResponse, *github.Response, error) {
			sentSHA = opts.SHA
			return &github.RepositoryContentResponse{Commit: github.Commit{SHA: github.String("created")}}, nil, nil
		}

		publisher := NewGitHubPublisherWithClient(mock)
		sha, err := publisher.CreateOrUpdateFile(ctx, "owner", "repo", "new.yaml", "main", "publish", []byte("body"))
		require.NoError(t, err)
		assert.Equal(t, "created", sha)
		assert.Nil(t, sentSHA, "a create carries no SHA")
	})
}
