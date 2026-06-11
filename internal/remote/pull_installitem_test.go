package remote

import (
	"bytes"
	"context"
	"os"
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
	}
	opts = append(opts, extra...)
	return NewPuller(registry, AuthConfig{}, opts...)
}

func TestInstallPulledItem(t *testing.T) {
	t.Run("profile is locked as a reference, not materialized, without cascade", func(t *testing.T) {
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
		assert.Equal(t, "<remote>:alice/myprofile@sha-prof", res.LocalPath,
			"profiles get a synthetic path, not a disk write")

		// A remote profile is a pure reference — nothing is materialized to disk.
		_, statErr := fs.Stat(ref.LocalPath("/test", ItemTypeProfile))
		assert.True(t, os.IsNotExist(statErr), "the profile must NOT be written to disk")

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

// TestInstallPulledItem_StageUntrustedNew pins the non-interactive security
// gate: with StageUntrustedNew (set by sync, whose pulls are Blind and so skip
// the interactive security review), a FIRST install from an untrusted remote
// must land in the pending lockfile — never the active one — so its hooks/MCP/
// context cannot activate until the user approves it. Trusted remotes and
// already-locked items keep today's behavior.
func TestInstallPulledItem_StageUntrustedNew(t *testing.T) {
	const bundleKey = "https://github.com/alice/ctxloom@bundles/security"
	newItem := func(rem *Remote) *fetchedItem {
		return &fetchedItem{rem: rem, localName: bundleKey, sha: "sha-new", content: []byte("description: a bundle\n")}
	}
	stagedOpts := func(itemType ItemType, out *bytes.Buffer) PullOptions {
		return PullOptions{ItemType: itemType, LocalDir: "/test", Blind: true, StageUntrustedNew: true, Stdout: out, Stdin: strings.NewReader("")}
	}

	t.Run("untrusted first install is staged in pending, not active", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		active := NewLockfileManager("/test", WithLockfileFS(fs))
		pending := NewLockfileManager("/test", WithLockfileFS(fs), WithPendingLockfile())
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(active))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		var out bytes.Buffer
		res, err := puller.installPulledItem(context.Background(), ref, stagedOpts(ItemTypeBundle, &out), newItem(installItemRemote))
		require.NoError(t, err)
		assert.True(t, res.Staged, "the result must report the staging redirect")

		activeLock, _ := active.Load()
		_, inActive := activeLock.GetEntry(ItemTypeBundle, bundleKey)
		assert.False(t, inActive, "an unreviewed first install must NOT reach the active lockfile")

		pendingLock, _ := pending.Load()
		entry, inPending := pendingLock.GetEntry(ItemTypeBundle, bundleKey)
		require.True(t, inPending, "the first install is staged for review")
		assert.Equal(t, "sha-new", entry.SHA)
	})

	t.Run("trusted first install goes straight to active", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		require.NoError(t, registry.SetTrustBundles("alice", true))
		trusted, err := registry.Get("alice")
		require.NoError(t, err)
		require.True(t, trusted.TrustBundles)

		active := NewLockfileManager("/test", WithLockfileFS(fs))
		pending := NewLockfileManager("/test", WithLockfileFS(fs), WithPendingLockfile())
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(active))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		var out bytes.Buffer
		res, err := puller.installPulledItem(context.Background(), ref, stagedOpts(ItemTypeBundle, &out), newItem(trusted))
		require.NoError(t, err)
		assert.False(t, res.Staged)

		activeLock, _ := active.Load()
		entry, inActive := activeLock.GetEntry(ItemTypeBundle, bundleKey)
		require.True(t, inActive, "trust means apply without review — same as before")
		assert.Equal(t, "sha-new", entry.SHA)

		pendingLock, _ := pending.Load()
		assert.True(t, pendingLock.IsEmpty(), "nothing staged for a trusted remote")
	})

	t.Run("already-locked item updates active even when untrusted", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		active := NewLockfileManager("/test", WithLockfileFS(fs))
		seeded := &Lockfile{Version: 1, Bundles: map[string]LockEntry{}, Profiles: map[string]LockEntry{}}
		seeded.AddEntry(ItemTypeBundle, bundleKey, LockEntry{SHA: "sha-old", URL: "https://github.com/alice/ctxloom"})
		require.NoError(t, active.Save(seeded))
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(active))

		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "security"}
		var out bytes.Buffer
		res, err := puller.installPulledItem(context.Background(), ref, stagedOpts(ItemTypeBundle, &out), newItem(installItemRemote))
		require.NoError(t, err)
		assert.False(t, res.Staged, "an item the user already has locked is not a first install")

		activeLock, _ := active.Load()
		entry, _ := activeLock.GetEntry(ItemTypeBundle, bundleKey)
		assert.Equal(t, "sha-new", entry.SHA, "sync still installs the pinned set for known items")
	})

	t.Run("untrusted first-install profile is staged too", func(t *testing.T) {
		fs, registry := installItemEnv(t)
		active := NewLockfileManager("/test", WithLockfileFS(fs))
		pending := NewLockfileManager("/test", WithLockfileFS(fs), WithPendingLockfile())
		puller := newInstallPuller(registry, fs, newMockFetcher(), WithLockfileManager(active))

		const profileKey = "https://github.com/alice/ctxloom@profiles/dev"
		ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeProfile, Path: "dev"}
		item := &fetchedItem{rem: installItemRemote, localName: profileKey, sha: "sha-prof", content: []byte("bundles: []\n")}
		var out bytes.Buffer
		res, err := puller.installPulledItem(context.Background(), ref, stagedOpts(ItemTypeProfile, &out), item)
		require.NoError(t, err)
		assert.True(t, res.Staged)

		activeLock, _ := active.Load()
		_, inActive := activeLock.GetEntry(ItemTypeProfile, profileKey)
		assert.False(t, inActive)
		pendingLock, _ := pending.Load()
		_, inPending := pendingLock.GetEntry(ItemTypeProfile, profileKey)
		assert.True(t, inPending)
	})
}
