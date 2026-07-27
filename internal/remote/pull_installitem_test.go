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

	// U094-F05: for a bundle the lockfile is the ONLY on-disk record — bundles
	// are never written to disk (writePulledContent is a synthetic no-op). A
	// failed lockfile write used to be demoted to a printed "Warning:" while
	// the pull still reported success, so a caller was told a SHA and
	// LocalPath for a pin that does not exist anywhere — and on a retracted
	// item, the freshly-computed Retracted verdict was lost right along with
	// it, leaving EffectiveTrust nothing to withhold against. The lockfile
	// write failing must fail the pull.
	t.Run("lockfile write failure fails the install, not just a warning", func(t *testing.T) {
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
		require.Error(t, err, "a pull whose only persistent record failed to write must not report success")
		assert.Nil(t, res)
	})
}
