package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

func TestPendingLockfileLifecycle(t *testing.T) {
	// These helpers exercise the lock.pending.yaml ↔ lock.yaml transitions
	// that drive the review-flow tools (acknowledge / decline / trust).

	mkCfg := func(t *testing.T) *config.Config {
		t.Helper()
		return &config.Config{AppPaths: []string{t.TempDir()}}
	}

	writePending := func(t *testing.T, cfg *config.Config, entries map[string]string) {
		t.Helper()
		mgr := remote.NewLockfileManager(cfg.AppPaths[0], remote.WithPendingLockfile())
		lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{}, Profiles: map[string]remote.LockEntry{}}
		for name, sha := range entries {
			lock.Bundles[name] = remote.LockEntry{SHA: sha, URL: "https://example.com/r"}
		}
		require.NoError(t, mgr.Save(lock))
	}

	readActive := func(t *testing.T, cfg *config.Config) *remote.Lockfile {
		t.Helper()
		mgr := remote.NewLockfileManager(cfg.AppPaths[0])
		lock, err := mgr.Load()
		require.NoError(t, err)
		return lock
	}

	pendingExists := func(t *testing.T, cfg *config.Config) bool {
		t.Helper()
		mgr := remote.NewLockfileManager(cfg.AppPaths[0], remote.WithPendingLockfile())
		lock, err := mgr.Load()
		require.NoError(t, err)
		return !lock.IsEmpty()
	}

	t.Run("MergePendingLockfileCount moves every bundle and deletes pending", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{
			"r/a": "sha1",
			"r/b": "sha2",
		})

		merged, err := MergePendingLockfileCount(cfg)
		require.NoError(t, err)
		assert.Equal(t, 2, merged)

		active := readActive(t, cfg)
		assert.Len(t, active.Bundles, 2)
		assert.False(t, pendingExists(t, cfg), "pending should be deleted after merge")
	})

	t.Run("MergePendingLockfileCount on empty pending is a no-op", func(t *testing.T) {
		cfg := mkCfg(t)
		merged, err := MergePendingLockfileCount(cfg)
		require.NoError(t, err)
		assert.Zero(t, merged)
	})

	t.Run("PromotePendingBundles moves only named bundles", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{
			"r/a": "sha1",
			"r/b": "sha2",
			"r/c": "sha3",
		})

		require.NoError(t, PromotePendingBundles(cfg, []string{"r/a", "r/c"}))

		active := readActive(t, cfg)
		assert.Contains(t, active.Bundles, "r/a")
		assert.Contains(t, active.Bundles, "r/c")
		assert.NotContains(t, active.Bundles, "r/b")
		assert.True(t, pendingExists(t, cfg), "r/b is still pending")
	})

	t.Run("DropPendingBundle removes one entry", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{
			"r/a": "sha1",
			"r/b": "sha2",
		})

		found, err := DropPendingBundle(cfg, "r/a")
		require.NoError(t, err)
		assert.True(t, found)

		// "r/b" remains
		assert.True(t, pendingExists(t, cfg))

		// Active untouched
		assert.Empty(t, readActive(t, cfg).Bundles)
	})

	t.Run("DropPendingBundle for unknown name returns false and no error", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{"r/a": "sha1"})

		found, err := DropPendingBundle(cfg, "nope/missing")
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("DropPendingBundle deletes pending file when last entry leaves", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{"r/a": "sha1"})

		found, err := DropPendingBundle(cfg, "r/a")
		require.NoError(t, err)
		assert.True(t, found)
		assert.False(t, pendingExists(t, cfg))
	})

	t.Run("ClearPendingLockfile drops everything", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{
			"r/a": "sha1",
			"r/b": "sha2",
		})
		require.NoError(t, ClearPendingLockfile(cfg))
		assert.False(t, pendingExists(t, cfg))
	})

	t.Run("LoadPendingLockfile returns nil when absent", func(t *testing.T) {
		cfg := mkCfg(t)
		lock, err := LoadPendingLockfile(cfg)
		require.NoError(t, err)
		assert.Nil(t, lock)
	})

	t.Run("LoadPendingLockfile returns the lockfile when present", func(t *testing.T) {
		cfg := mkCfg(t)
		writePending(t, cfg, map[string]string{"r/a": "sha1"})
		lock, err := LoadPendingLockfile(cfg)
		require.NoError(t, err)
		require.NotNil(t, lock)
		assert.Len(t, lock.Bundles, 1)
	})

	writeActive := func(t *testing.T, cfg *config.Config, entries map[string]string) {
		t.Helper()
		mgr := remote.NewLockfileManager(cfg.AppPaths[0])
		lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{}, Profiles: map[string]remote.LockEntry{}}
		for name, sha := range entries {
			lock.Bundles[name] = remote.LockEntry{SHA: sha, URL: "https://example.com/r"}
		}
		require.NoError(t, mgr.Save(lock))
	}

	t.Run("SetBundlePin flips the flag and persists", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		found, err := SetBundlePin(cfg, "r/a", true)
		require.NoError(t, err)
		assert.True(t, found)

		active := readActive(t, cfg)
		require.Contains(t, active.Bundles, "r/a")
		assert.True(t, active.Bundles["r/a"].Pinned)
	})

	t.Run("SetBundlePin idempotent on repeated true", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		_, _ = SetBundlePin(cfg, "r/a", true)
		found, err := SetBundlePin(cfg, "r/a", true)
		require.NoError(t, err, "second pin must not error")
		assert.True(t, found)
	})

	t.Run("SetBundlePin unpin clears the flag", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		_, _ = SetBundlePin(cfg, "r/a", true)
		found, err := SetBundlePin(cfg, "r/a", false)
		require.NoError(t, err)
		assert.True(t, found)

		active := readActive(t, cfg)
		assert.False(t, active.Bundles["r/a"].Pinned)
	})

	t.Run("SetBundlePin unknown bundle returns false, no error", func(t *testing.T) {
		// "Not in the active lockfile" is a user-visible state, not a
		// programmer error. The MCP handler turns this into a friendly
		// message.
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		found, err := SetBundlePin(cfg, "r/never-existed", true)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("LoadActiveLockfile mirrors LoadPendingLockfile", func(t *testing.T) {
		cfg := mkCfg(t)
		writeActive(t, cfg, map[string]string{"r/a": "sha1"})

		lock, err := LoadActiveLockfile(cfg)
		require.NoError(t, err)
		require.NotNil(t, lock)
		assert.Len(t, lock.Bundles, 1)
	})
}
