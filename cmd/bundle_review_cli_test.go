package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// holdItem drives `bundle hold`. The load-bearing property: the pin is checked
// BEFORE the pending lockfile is touched, so a bundle that exists only as a
// staged NEW change is never silently dropped without approval or decline.
func TestHoldItem(t *testing.T) {
	mkCfg := func(t *testing.T) *config.Config {
		t.Helper()
		return &config.Config{AppPaths: []string{t.TempDir()}}
	}
	writeBundles := func(t *testing.T, cfg *config.Config, pending bool, entries map[string]string) {
		t.Helper()
		var opts []remote.LockfileOption
		if pending {
			opts = append(opts, remote.WithPendingLockfile())
		}
		mgr := remote.NewLockfileManager(cfg.AppPaths[0], opts...)
		lock := &remote.Lockfile{Bundles: map[string]remote.LockEntry{}, Profiles: map[string]remote.LockEntry{}}
		for name, sha := range entries {
			lock.Bundles[name] = remote.LockEntry{SHA: sha, URL: "https://example.com/r"}
		}
		require.NoError(t, mgr.Save(lock))
	}
	loadBundles := func(t *testing.T, cfg *config.Config, pending bool) map[string]remote.LockEntry {
		t.Helper()
		var opts []remote.LockfileOption
		if pending {
			opts = append(opts, remote.WithPendingLockfile())
		}
		lock, err := remote.NewLockfileManager(cfg.AppPaths[0], opts...).Load()
		require.NoError(t, err)
		return lock.Bundles
	}

	t.Run("pending-only bundle is left staged, not destroyed", func(t *testing.T) {
		cfg := mkCfg(t)
		writeBundles(t, cfg, true, map[string]string{"r/new": "sha1"})

		var out bytes.Buffer
		held, err := holdItem(cfg, "r/new", &out)
		require.NoError(t, err)
		assert.False(t, held, "a pending-only bundle has no active entry to hold")
		assert.Contains(t, out.String(), "only staged for review")

		// The staged review item must survive for an explicit approve/decline.
		assert.Contains(t, loadBundles(t, cfg, true), "r/new")
	})

	t.Run("active bundle is pinned and its pending change dropped", func(t *testing.T) {
		cfg := mkCfg(t)
		writeBundles(t, cfg, false, map[string]string{"r/a": "oldsha"})
		writeBundles(t, cfg, true, map[string]string{"r/a": "newsha"})

		var out bytes.Buffer
		held, err := holdItem(cfg, "r/a", &out)
		require.NoError(t, err)
		assert.True(t, held)
		assert.Contains(t, out.String(), "Held")

		active := loadBundles(t, cfg, false)
		require.Contains(t, active, "r/a")
		assert.True(t, active["r/a"].Pinned)
		assert.NotContains(t, loadBundles(t, cfg, true), "r/a", "the held change must not re-surface for review")
	})

	t.Run("unknown name reports nothing to hold", func(t *testing.T) {
		cfg := mkCfg(t)
		writeBundles(t, cfg, false, map[string]string{"r/a": "sha1"})

		var out bytes.Buffer
		held, err := holdItem(cfg, "r/never-existed", &out)
		require.NoError(t, err)
		assert.False(t, held)
		assert.Contains(t, out.String(), "not in the active lockfile")
	})
}
