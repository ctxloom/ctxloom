package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubVCS is a VCS double implementing the optional Versioned capability,
// recording the last read so tests can assert path/revision routing.
type stubVCS struct {
	data           []byte
	err            error
	lastPath       string
	lastRev        string
	readFileCalled bool
}

func (s *stubVCS) ReadFile(_ context.Context, path string) ([]byte, error) {
	s.readFileCalled = true
	s.lastPath, s.lastRev = path, ""
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

func (s *stubVCS) ReadFileAt(_ context.Context, path, rev string) ([]byte, error) {
	s.lastPath, s.lastRev = path, rev
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

func (s *stubVCS) ResolveRevision(_ context.Context, rev string) (string, error) {
	return rev, nil
}

var (
	_ VCS       = (*stubVCS)(nil)
	_ Versioned = (*stubVCS)(nil)
)

// stubRefFetcher records whether it was dispatched to, for the given predicate.
type stubRefFetcher struct {
	handles func(*Reference) bool
	called  bool
}

func (s *stubRefFetcher) Handles(ref *Reference) bool { return s.handles(ref) }
func (s *stubRefFetcher) FetchItem(_ context.Context, _ *Reference, _ string) ([]byte, error) {
	s.called = true
	return []byte("stub"), nil
}

func canonicalBundleRef(t *testing.T) *Reference {
	t.Helper()
	ref, err := ParseReference("https://github.com/owner/repo@bundles/core-practices")
	require.NoError(t, err)
	require.True(t, ref.IsCanonical())
	return ref
}

func TestRemoteRefFetcher_FetchItem_Pinned(t *testing.T) {
	ctx := context.Background()
	vcs := &stubVCS{data: []byte("name: core-practices")}
	f := NewRemoteRefFetcher(func(loc string) (VCS, error) {
		assert.Equal(t, "https://github.com/owner/repo", loc)
		return vcs, nil
	})

	ref := canonicalBundleRef(t)
	ref.ContentVersion = "v2.0.0"

	data, err := NewResolver(f).Resolve(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, []byte("name: core-practices"), data)
	// The canonical item path and pinned (opaque) revision flow through to the VCS.
	assert.Equal(t, "ctxloom/bundles/core-practices.yaml", vcs.lastPath)
	assert.Equal(t, "v2.0.0", vcs.lastRev)
	assert.False(t, vcs.readFileCalled, "a pinned ref takes the Versioned path, not current")
}

func TestRemoteRefFetcher_FetchItem_Versionless(t *testing.T) {
	ctx := context.Background()
	vcs := &stubVCS{data: []byte("name: core-practices")}
	f := NewRemoteRefFetcher(func(string) (VCS, error) { return vcs, nil })

	data, err := f.FetchItem(ctx, canonicalBundleRef(t), "")
	require.NoError(t, err)
	assert.Equal(t, []byte("name: core-practices"), data)
	assert.True(t, vcs.readFileCalled, "a versionless ref reads the current state")
}

func TestRemoteRefFetcher_FetchItem_ThroughGitBackend(t *testing.T) {
	ctx := context.Background()
	mf := NewMockFetcher().WithFile("ctxloom/bundles/core-practices.yaml", []byte("name: core-practices"))
	f := NewRemoteRefFetcher(GitForgeVCSFactory(mockFetcherFactory(mf), AuthConfig{}))

	data, err := f.FetchItem(ctx, canonicalBundleRef(t), "")
	require.NoError(t, err)
	assert.Equal(t, []byte("name: core-practices"), data)
}

func TestRemoteRefFetcher_Handles(t *testing.T) {
	f := NewRemoteRefFetcher(func(string) (VCS, error) { return &stubVCS{}, nil })

	assert.True(t, f.Handles(canonicalBundleRef(t)), "canonical URL ref is handled")

	local, err := ParseReference("ctxloom:local@bundles/foo")
	require.NoError(t, err)
	assert.False(t, f.Handles(local), "a local ref is not a remote-fetcher concern")
	assert.False(t, f.Handles(nil))
	assert.False(t, f.Handles(&Reference{ItemType: ItemTypeBundle, Path: "x"}), "a URL-less ref is not canonical, so not handled")
}

func TestRemoteRefFetcher_FetchItem_RejectsUnhandled(t *testing.T) {
	f := NewRemoteRefFetcher(func(string) (VCS, error) { return &stubVCS{}, nil })
	local, err := ParseReference("ctxloom:local@bundles/foo")
	require.NoError(t, err)

	_, err = f.FetchItem(context.Background(), local, "")
	assert.Error(t, err)
}

func TestRemoteRefFetcher_FetchItem_PropagatesReadError(t *testing.T) {
	vcs := &stubVCS{err: errors.New("boom")}
	f := NewRemoteRefFetcher(func(string) (VCS, error) { return vcs, nil })

	_, err := f.FetchItem(context.Background(), canonicalBundleRef(t), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestResolver_DispatchesByScheme(t *testing.T) {
	ctx := context.Background()

	// local stub claims everything non-canonical; remote claims canonical URLs.
	local := &stubRefFetcher{handles: func(r *Reference) bool { return r != nil && !r.IsCanonical() }}
	remoteVCS := &stubVCS{data: []byte("remote")}
	remote := NewRemoteRefFetcher(func(string) (VCS, error) { return remoteVCS, nil })

	resolver := NewResolver(local, remote)

	// Canonical → remote.
	data, err := resolver.Resolve(ctx, canonicalBundleRef(t))
	require.NoError(t, err)
	assert.Equal(t, []byte("remote"), data)
	assert.False(t, local.called, "local must not be invoked for a canonical ref")

	// Local (non-canonical) → local stub.
	localRef, err := ParseReference("ctxloom:local@bundles/foo")
	require.NoError(t, err)
	data, err = resolver.Resolve(ctx, localRef)
	require.NoError(t, err)
	assert.Equal(t, []byte("stub"), data)
	assert.True(t, local.called)
}

func TestResolver_NoFetcher(t *testing.T) {
	_, err := NewResolver().Resolve(context.Background(), canonicalBundleRef(t))
	assert.Error(t, err)
}

func TestResolver_NilReference(t *testing.T) {
	_, err := NewResolver().Resolve(context.Background(), nil)
	assert.Error(t, err)
}
