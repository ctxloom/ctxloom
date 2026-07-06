package remote

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var installItemRemote = &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

// installItemEnv builds an in-memory fs and a registry holding the "alice"
// remote — the shared scaffolding for driving installPulledItem branch by
// branch without going through the full Pull fetch phase.
func installItemEnv(t *testing.T) (afero.Fs, *Registry) {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/test", 0755))
	registry, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))
	return fs, registry
}

func newInstallPuller(registry *Registry, fs afero.Fs, mf *mockFetcher, extra ...PullerOption) *Puller {
	opts := []PullerOption{
		WithPullerFS(fs),
		WithFetcherFactory(mockFetcherFactory(mf)),
	}
	opts = append(opts, extra...)
	return NewPuller(registry, AuthConfig{}, opts...)
}

func TestInstallPulledItem(t *testing.T) {
	const bundleKey = "https://github.com/alice/ctxloom@bundles/security"

	t.Run("bundle pin lands in the active lockfile", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		active := NewLockfileManager("/test", WithLockfileFS(fs))
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(active))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: bundleKey,
			sha:       "sha-bundle",
			content:   []byte("description: a bundle\n"),
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err)
		assert.Equal(t, "<remote>:"+bundleKey+"@sha-bundle", res.LocalPath, "bundles get a synthetic path, not a disk write")
		assert.False(t, res.Overwritten)

		activeLock, _ := active.Load()
		entry, inActive := activeLock.GetEntry(ItemTypeBundle, bundleKey)
		require.True(t, inActive, "the pin lands straight in the active lockfile")
		assert.Equal(t, "sha-bundle", entry.SHA)
	})

	t.Run("lockfile write failure warns but does not fail the install", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		// A read-only lockfile fs makes Save fail; the puller's own fs stays
		// writable (unused for bundles, which are not written to disk).
		roLock := NewLockfileManager("/test", WithLockfileFS(afero.NewReadOnlyFs(afero.NewMemMapFs())))
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(roLock))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: bundleKey,
			sha:       "sha-bundle",
			content:   []byte("description: a bundle\n"),
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err, "a lockfile failure must not fail the pull")
		assert.NotNil(t, res)
		assert.Contains(t, out.String(), "failed to update lockfile", "the failure is surfaced as a warning")
	})
}
