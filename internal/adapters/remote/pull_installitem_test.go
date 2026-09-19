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
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithTreeInstaller(stubTreeInstaller()),
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
			treeRoot:  (&Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}).TreeRepoPath(),
			// A TREE, not a document: item.tree == nil is exactly what
			// installPulledItem now refuses ("bundles are distributed as
			// trees"). A version-only manifest is a valid bundle to read
			// (Bundle.declaresNothing requires no version AND no items).
			tree: map[string]TreeFile{
				"bundle.yaml": {Data: []byte("version: \"1.0.0\"\n")},
			},
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err)
		assert.False(t, res.Overwritten)
		assert.Equal(t, ref.LocalTreePath("/test"), res.LocalPath,
			"a tree bundle reports the cache path its Reference derives — the directory inside its worktree")

		activeLock, _ := active.Load()
		entry, inActive := activeLock.GetEntry(ItemTypeBundle, bundleKey)
		require.True(t, inActive, "the pin lands straight in the active lockfile")
		assert.Equal(t, "sha-bundle", entry.SHA)
	})

	// The lockfile is the AUTHORITY on a bundle's pin even though a tree
	// bundle is also materialized to the (gitignored, regenerable) cache: the
	// cache is derived from the pin, not the other way around. A failed
	// lockfile write used to be demoted to a printed "Warning:" while the
	// pull still reported success, so a caller was told a SHA and LocalPath
	// for a pin that does not exist anywhere — and on a retracted item, the
	// freshly-computed Retracted verdict was lost right along with it,
	// leaving EffectiveTrust nothing to withhold against. The lockfile write
	// failing must fail the pull.
	t.Run("lockfile write failure fails the install, not just a warning", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		// A read-only lockfile fs: installTree also writes through
		// p.lockfileManager.FS() (the tree cache lives under the same base
		// dir the lockfile does), so this fails BOTH the tree materialization
		// and the lockfile Save — either is a persistent-write failure the
		// pull must not report success over.
		roLock := NewLockfileManager("/test", WithLockfileFS(afero.NewReadOnlyFs(afero.NewMemMapFs())))
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(roLock))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: bundleKey,
			sha:       "sha-bundle",
			tree: map[string]TreeFile{
				"bundle.yaml": {Data: []byte("version: \"1.0.0\"\n")},
			},
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.Error(t, err, "a pull whose only persistent record failed to write must not report success")
		assert.Nil(t, res)
	})
}
