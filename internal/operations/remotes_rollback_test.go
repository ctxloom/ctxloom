package operations

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// failAfterNWritesFs fails every write-opening OpenFile/Create call after the
// first n succeed. It lets a test allow the registry persist for Add and fail
// the one for SetTrustBundles within a single AddRemote call.
type failAfterNWritesFs struct {
	afero.Fs
	allowed int
	writes  int
}

func (f *failAfterNWritesFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		f.writes++
		if f.writes > f.allowed {
			return nil, fmt.Errorf("simulated write failure")
		}
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f *failAfterNWritesFs) Create(name string) (afero.File, error) {
	f.writes++
	if f.writes > f.allowed {
		return nil, fmt.Errorf("simulated write failure")
	}
	return f.Fs.Create(name)
}

// TestAddRemote_TrustFailureRollsBack pins the trust-on-add failure path to the
// same rollback contract as the three failure paths before it: when
// SetTrustBundles cannot persist, the freshly added remote must be removed so
// a retry of the same `remote add` does not fail on the duplicate name.
func TestAddRemote_TrustFailureRollsBack(t *testing.T) {
	fs := &failAfterNWritesFs{Fs: afero.NewMemMapFs(), allowed: 1} // Add persists; trust persist fails
	require.NoError(t, fs.Fs.MkdirAll(testBaseDir, 0o755))
	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	fetcher := remote.NewMockFetcher().WithValidRepo("alice", "ctxloom")

	_, err = AddRemote(context.Background(), nil, AddRemoteRequest{
		Name:     "alice",
		URL:      "https://github.com/alice/ctxloom",
		Trust:    true,
		Registry: registry,
		Fetcher:  fetcher,
		Cache:    &fakeCloner{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trust remote")

	assert.False(t, registry.Has("alice"),
		"a failed trust-on-add must roll the registration back so a retry can succeed")
}
