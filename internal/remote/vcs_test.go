package remote

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitVCS(t *testing.T, mf *MockFetcher) VCS {
	t.Helper()
	vcs, err := GitForgeVCSFactory(mockFetcherFactory(mf), AuthConfig{})("https://github.com/owner/repo")
	require.NoError(t, err)
	return vcs
}

func TestGitForgeVCS_IsVersioned(t *testing.T) {
	vcs := gitVCS(t, NewMockFetcher())
	_, ok := vcs.(Versioned)
	assert.True(t, ok, "git backend supports history")
}

func TestGitForgeVCS_ReadFile_Current(t *testing.T) {
	ctx := context.Background()
	mf := NewMockFetcher().WithFile("ctxloom/bundles/core.yaml", []byte("name: core"))

	data, err := gitVCS(t, mf).ReadFile(ctx, "ctxloom/bundles/core.yaml")
	require.NoError(t, err)
	assert.Equal(t, []byte("name: core"), data)

	require.Len(t, mf.FetchFileCalls, 1)
	call := mf.FetchFileCalls[0]
	assert.Equal(t, "owner", call.Owner)
	assert.Equal(t, "repo", call.Repo)
	assert.Equal(t, "ctxloom/bundles/core.yaml", call.Path)
	assert.Equal(t, "", call.Ref, "current read uses the default branch (empty ref)")
}

func TestGitForgeVCS_ReadFileAt_Revision(t *testing.T) {
	ctx := context.Background()
	mf := NewMockFetcher().WithFile("ctxloom/bundles/core.yaml", []byte("name: core"))

	v := gitVCS(t, mf).(Versioned)
	data, err := v.ReadFileAt(ctx, "ctxloom/bundles/core.yaml", "abc123")
	require.NoError(t, err)
	assert.Equal(t, []byte("name: core"), data)
	require.Len(t, mf.FetchFileCalls, 1)
	assert.Equal(t, "abc123", mf.FetchFileCalls[0].Ref)
}

func TestGitForgeVCS_ResolveRevision(t *testing.T) {
	ctx := context.Background()
	mf := NewMockFetcher().WithRef("v1.0.0", "deadbeef")

	v := gitVCS(t, mf).(Versioned)
	sha, err := v.ResolveRevision(ctx, "v1.0.0")
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", sha)
}

func TestGitForgeVCSFactory_InvalidURL(t *testing.T) {
	_, err := GitForgeVCSFactory(mockFetcherFactory(NewMockFetcher()), AuthConfig{})("not-a-valid-repo-url")
	assert.Error(t, err)
}

func TestFSVCS_ReadFile_Current(t *testing.T) {
	ctx := context.Background()
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/proj/.ctxloom/local/ctxloom/bundles/foo.yaml", []byte("name: foo"), 0o644))

	vcs, err := FSVCSFactory(fs)("/proj/.ctxloom/local")
	require.NoError(t, err)

	data, err := vcs.ReadFile(ctx, "ctxloom/bundles/foo.yaml")
	require.NoError(t, err)
	assert.Equal(t, []byte("name: foo"), data)
}

func TestFSVCS_IsNotVersioned(t *testing.T) {
	vcs, err := FSVCSFactory(afero.NewMemMapFs())("/proj/.ctxloom/local")
	require.NoError(t, err)
	_, ok := vcs.(Versioned)
	assert.False(t, ok, "a plain filesystem backend has no history")
}

func TestReadItemAt_VersionlessReadsCurrent(t *testing.T) {
	ctx := context.Background()
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/root/ctxloom/bundles/foo.yaml", []byte("cur"), 0o644))
	vcs, _ := FSVCSFactory(fs)("/root")

	data, err := readItemAt(ctx, vcs, "ctxloom/bundles/foo.yaml", "")
	require.NoError(t, err)
	assert.Equal(t, []byte("cur"), data)
}

func TestReadItemAt_PinnedAgainstCurrentOnlyErrors(t *testing.T) {
	ctx := context.Background()
	vcs, _ := FSVCSFactory(afero.NewMemMapFs())("/root")

	_, err := readItemAt(ctx, vcs, "ctxloom/bundles/foo.yaml", "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current/HEAD", "pinned read on a history-less backend is rejected, not silently served")
}

func TestReadItemAt_PinnedUsesVersioned(t *testing.T) {
	ctx := context.Background()
	mf := NewMockFetcher().WithFile("ctxloom/bundles/core.yaml", []byte("pinned"))
	vcs := gitVCS(t, mf)

	data, err := readItemAt(ctx, vcs, "ctxloom/bundles/core.yaml", "v9")
	require.NoError(t, err)
	assert.Equal(t, []byte("pinned"), data)
	require.Len(t, mf.FetchFileCalls, 1)
	assert.Equal(t, "v9", mf.FetchFileCalls[0].Ref)
}
