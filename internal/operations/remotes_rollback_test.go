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

// failAtWriteFs fails exactly the Nth write-opening OpenFile/Create call and
// lets every other write through. This fails only the SetTrustBundles persist
// (the 2nd registry save) within a single AddRemote call, while allowing both
// the initial Add persist (#1) and the rollback Remove persist (#3) — so the
// rollback genuinely clears the registration from disk and memory. (registry
// Remove rolls back its in-memory delete if its own save fails, so an fs that
// also failed the rollback write would leave the remote registered on disk
// anyway — an unrealistic all-writes-fail case, not the contract under test.)
type failAtWriteFs struct {
	afero.Fs
	failAt int
	writes int
}

func (f *failAtWriteFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		f.writes++
		if f.writes == f.failAt {
			return nil, fmt.Errorf("simulated write failure")
		}
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f *failAtWriteFs) Create(name string) (afero.File, error) {
	f.writes++
	if f.writes == f.failAt {
		return nil, fmt.Errorf("simulated write failure")
	}
	return f.Fs.Create(name)
}

// TestAddRemote_TrustFailureRollsBack pins the trust-on-add failure path to the
// same rollback contract as the three failure paths before it: when
// SetTrustBundles cannot persist, the freshly added remote must be removed so
// a retry of the same `remote add` does not fail on the duplicate name.
func TestAddRemote_TrustFailureRollsBack(t *testing.T) {
	fs := &failAtWriteFs{Fs: afero.NewMemMapFs(), failAt: 2} // Add (#1) persists; trust persist (#2) fails; rollback Remove (#3) persists
	require.NoError(t, fs.MkdirAll(testBaseDir, 0o755))
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
