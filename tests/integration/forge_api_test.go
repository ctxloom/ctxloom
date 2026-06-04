//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive the go-github fetcher through a real HTTP layer (the
// in-process ForgeStub) to pin forge-error-shape detection: a 404 must surface
// as errs.ErrRemoteContentNotFound and a 200 must round-trip content. Non-GitHub
// hosts use the generic git-clone fetcher (covered in remote_transport_test.go),
// so only the GitHub API envelope is exercised here.

func newGitHubFetcher(t *testing.T, stub *testenv.ForgeStub) *remote.GitHubFetcher {
	t.Helper()
	// A non-empty token plus an injected http client: the injected client wins
	// and (per the constructor) no auth fallback is created, so a stub 4xx is
	// returned verbatim rather than retried.
	return remote.NewGitHubFetcher("test-token", remote.WithHTTPClient(stub.GitHubHTTPClient()))
}

func TestForgeAPI_GitHub_NotFoundMapsToSentinel(t *testing.T) {
	stub := testenv.NewForgeStub(t)
	stub.AddFile("ctxloom/bundles/present.yaml", []byte("version: 1.0.0\n"))
	f := newGitHubFetcher(t, stub)
	ctx := context.Background()

	t.Run("FetchFile happy path round-trips content", func(t *testing.T) {
		data, err := f.FetchFile(ctx, "owner", "repo", "ctxloom/bundles/present.yaml", "main")
		require.NoError(t, err)
		assert.Equal(t, "version: 1.0.0\n", string(data))
	})

	t.Run("FetchFile 404 -> ErrRemoteContentNotFound", func(t *testing.T) {
		_, err := f.FetchFile(ctx, "owner", "repo", "ctxloom/bundles/missing.yaml", "main")
		require.Error(t, err)
		assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound)
	})

	t.Run("ListDir 404 -> ErrRemoteContentNotFound", func(t *testing.T) {
		_, err := f.ListDir(ctx, "owner", "repo", "ctxloom/nonexistent", "main")
		require.Error(t, err)
		assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound)
	})

	t.Run("ResolveRef unknown ref -> ErrRemoteContentNotFound", func(t *testing.T) {
		_, err := f.ResolveRef(ctx, "owner", "repo", "no-such-ref")
		require.Error(t, err)
		assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound)
	})

	t.Run("ListDir lists registered directory entries", func(t *testing.T) {
		entries, err := f.ListDir(ctx, "owner", "repo", "ctxloom/bundles", "main")
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name)
		}
		assert.Contains(t, names, "present.yaml")
	})
}

// TestForgeAPI_Unauthorized confirms a 401 envelope is surfaced as an error
// (not silently treated as "not found"). The GitHub helper is is401Error.
func TestForgeAPI_Unauthorized(t *testing.T) {
	ctx := context.Background()

	t.Run("github", func(t *testing.T) {
		stub := testenv.NewForgeStub(t)
		stub.Unauthorized = true
		f := newGitHubFetcher(t, stub)
		_, err := f.FetchFile(ctx, "owner", "repo", "ctxloom/bundles/x.yaml", "main")
		require.Error(t, err)
		// A 401 is not a 404 — it must NOT be mistaken for missing content.
		assert.NotErrorIs(t, err, errs.ErrRemoteContentNotFound)
	})
}
