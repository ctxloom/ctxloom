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
		WithTerminalChecker(&mockTerminalChecker{}),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithReplaceManager(nil),
		WithVendorManager(nil),
	}
	opts = append(opts, extra...)
	return NewPuller(registry, AuthConfig{}, opts...)
}

func TestInstallPulledItem(t *testing.T) {
	t.Run("profile is transformed, written, and locked without cascade", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		lm := NewLockfileManager("/test", WithLockfileFS(fs))
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(lm))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeProfile, Path: "myprofile"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: "alice/myprofile",
			sha:       "sha-prof",
			content:   []byte("bundles:\n  - https://github.com/alice/ctxloom@bundles/security\n"),
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeProfile, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err)
		assert.Nil(t, res.CascadePulled, "cascade disabled → no cascaded items")

		written, rerr := afero.ReadFile(fs, ref.LocalPath("/test", ItemTypeProfile))
		require.NoError(t, rerr, "a profile is written to disk")
		assert.Contains(t, string(written), "https://github.com/alice/ctxloom@bundles/security")

		lock, lerr := lm.Load()
		require.NoError(t, lerr)
		entry, ok := lock.GetEntry(ItemTypeProfile, "alice/myprofile")
		require.True(t, ok, "profile provenance is recorded in the lockfile")
		assert.Equal(t, "sha-prof", entry.SHA)
	})

	t.Run("bundle lockfile entry routes to the pending target when set", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		mainLock := NewLockfileManager("/test", WithLockfileFS(fs))
		pending := NewLockfileManager("/test", WithLockfileFS(fs), WithPendingLockfile())
		puller := newInstallPuller(registry, fs, newMockFetcher(),
			WithLockfileManager(mainLock), WithBundleLockfileTarget(pending))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: "https://github.com/alice/ctxloom@bundles/security",
			sha:       "sha-bundle",
			content:   []byte("description: a bundle\n"),
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err)
		assert.Equal(t, "<remote>:https://github.com/alice/ctxloom@bundles/security@sha-bundle", res.LocalPath, "bundles get a synthetic path, not a disk write")
		assert.False(t, res.Overwritten)

		pendingLock, _ := pending.Load()
		_, inPending := pendingLock.GetEntry(ItemTypeBundle, "https://github.com/alice/ctxloom@bundles/security")
		assert.True(t, inPending, "the bundle entry lands in the pending lockfile")

		mainLockData, _ := mainLock.Load()
		_, inMain := mainLockData.GetEntry(ItemTypeBundle, "https://github.com/alice/ctxloom@bundles/security")
		assert.False(t, inMain, "the bundle entry must NOT land in the main lockfile")
	})

	t.Run("cascade pulls the profile's referenced bundles", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		mf := newMockFetcher()
		mf.files["ctxloom/bundles/security.yaml"] = []byte("description: Security bundle\n")
		lm := NewLockfileManager("/test", WithLockfileFS(fs))
		puller := newInstallPuller(registry, fs, mf, WithLockfileManager(lm))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeProfile, Path: "myprofile"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: "alice/myprofile",
			sha:       "sha-prof",
			content:   []byte("bundles:\n  - https://github.com/alice/ctxloom@bundles/security\n"),
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeProfile, Cascade: true, Force: true, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err)
		assert.Contains(t, res.CascadePulled, "https://github.com/alice/ctxloom@bundles/security",
			"cascade must pull the bundle the profile references")
	})

	t.Run("cascade failure is wrapped and surfaced", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		// mock fetcher serves NO bundle file, so the cascaded bundle pull fails.
		lm := NewLockfileManager("/test", WithLockfileFS(fs))
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(lm))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeProfile, Path: "myprofile"}
		item := &fetchedItem{
			rem:       installItemRemote,
			localName: "alice/myprofile",
			sha:       "sha-prof",
			content:   []byte("bundles:\n  - https://github.com/alice/ctxloom@bundles/ghost\n"),
		}
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeProfile, Cascade: true, Force: true, LocalDir: "/test", Stdout: &out, Stdin: strings.NewReader("")}

		res, err := puller.installPulledItem(context.Background(), ref, opts, item)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cascade pull failed")
		assert.NotNil(t, res, "the profile itself installed; the partial result is still returned")
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
			localName: "https://github.com/alice/ctxloom@bundles/security",
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
