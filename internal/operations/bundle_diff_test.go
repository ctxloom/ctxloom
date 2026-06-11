package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

func TestDiffLockfiles(t *testing.T) {
	mkLock := func(bundles map[string]string) *remote.Lockfile {
		l := &remote.Lockfile{Bundles: map[string]remote.LockEntry{}, Profiles: map[string]remote.LockEntry{}}
		for name, sha := range bundles {
			l.Bundles[name] = remote.LockEntry{SHA: sha}
		}
		return l
	}

	t.Run("nil curr → empty change set", func(t *testing.T) {
		cs := DiffLockfiles(mkLock(map[string]string{"a/x": "111"}), nil, nil)
		assert.True(t, cs.IsEmpty())
	})

	t.Run("nil prev → everything is Added", func(t *testing.T) {
		cs := DiffLockfiles(nil, mkLock(map[string]string{
			"a/x": "111",
			"b/y": "222",
		}), nil)
		assert.Len(t, cs.Added, 2)
		assert.Empty(t, cs.Modified)
	})

	t.Run("same SHAs → empty change set", func(t *testing.T) {
		prev := mkLock(map[string]string{"a/x": "111"})
		curr := mkLock(map[string]string{"a/x": "111"})
		cs := DiffLockfiles(prev, curr, nil)
		assert.True(t, cs.IsEmpty())
	})

	t.Run("different SHA → Modified", func(t *testing.T) {
		prev := mkLock(map[string]string{"a/x": "111"})
		curr := mkLock(map[string]string{"a/x": "222"})
		cs := DiffLockfiles(prev, curr, nil)
		require.Len(t, cs.Modified, 1)
		assert.Equal(t, "a/x", cs.Modified[0].Name)
		assert.Equal(t, "111", cs.Modified[0].OldSHA)
		assert.Equal(t, "222", cs.Modified[0].NewSHA)
		assert.Empty(t, cs.Added)
	})

	t.Run("new bundle → Added", func(t *testing.T) {
		prev := mkLock(map[string]string{"a/x": "111"})
		curr := mkLock(map[string]string{
			"a/x": "111",
			"b/y": "333",
		})
		cs := DiffLockfiles(prev, curr, nil)
		assert.Empty(t, cs.Modified)
		require.Len(t, cs.Added, 1)
		assert.Equal(t, "b/y", cs.Added[0].Name)
		assert.Equal(t, "333", cs.Added[0].NewSHA)
	})

	t.Run("trusted remote filtered out", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		require.NoError(t, registry.Add("trusted", "https://github.com/trusted/repo"))
		require.NoError(t, registry.SetTrustBundles("trusted", true))
		require.NoError(t, registry.Add("untrusted", "https://github.com/untrusted/repo"))

		const (
			trustedKey   = "https://github.com/trusted/repo@bundles/x"
			untrustedKey = "https://github.com/untrusted/repo@bundles/y"
		)
		prev := mkLock(nil)
		curr := mkLock(map[string]string{
			trustedKey:   "111",
			untrustedKey: "222",
		})
		cs := DiffLockfiles(prev, curr, registry)
		require.Len(t, cs.Added, 1)
		assert.Equal(t, untrustedKey, cs.Added[0].Name, "trusted remote should be filtered out")
	})

	t.Run("removed bundles are NOT reported", func(t *testing.T) {
		prev := mkLock(map[string]string{
			"a/x": "111",
			"b/y": "222",
		})
		curr := mkLock(map[string]string{"a/x": "111"})
		cs := DiffLockfiles(prev, curr, nil)
		assert.True(t, cs.IsEmpty(), "removed bundles do not surface in review")
	})

	t.Run("pinned active entry suppresses Modified", func(t *testing.T) {
		// User pinned alice/security at SHA aaa. Even though pending
		// records a new SHA, the review template must not surface it.
		prev := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
			"alice/security": {SHA: "aaa", Pinned: true},
		}}
		curr := mkLock(map[string]string{"alice/security": "bbb"})
		cs := DiffLockfiles(prev, curr, nil)
		assert.True(t, cs.IsEmpty(), "pinned entries must not generate review noise")
	})

	t.Run("pinned active entry does NOT suppress unrelated bundles", func(t *testing.T) {
		// Pinning one bundle must not silence the rest.
		prev := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
			"alice/security": {SHA: "aaa", Pinned: true},
			"bob/other":      {SHA: "ccc"},
		}}
		curr := mkLock(map[string]string{
			"alice/security": "bbb",
			"bob/other":      "ddd",
		})
		cs := DiffLockfiles(prev, curr, nil)
		require.Len(t, cs.Modified, 1)
		assert.Equal(t, "bob/other", cs.Modified[0].Name, "only the un-pinned bundle surfaces")
	})

	// Profile lockfile entries are merged into active by ApproveUpgrade exactly
	// like bundles, so the review diff must surface them — a staged remote
	// profile change the review never showed would be applied sight-unseen.
	t.Run("new profile entry → Added with profile kind", func(t *testing.T) {
		prev := mkLock(map[string]string{"a/x": "111"})
		curr := mkLock(map[string]string{"a/x": "111"})
		curr.Profiles["https://github.com/o/r@profiles/dev"] = remote.LockEntry{SHA: "p11"}
		cs := DiffLockfiles(prev, curr, nil)
		assert.Empty(t, cs.Modified)
		require.Len(t, cs.Added, 1)
		assert.Equal(t, "https://github.com/o/r@profiles/dev", cs.Added[0].Name)
		assert.Equal(t, remote.ItemTypeProfile, cs.Added[0].Kind)
		assert.Equal(t, "p11", cs.Added[0].NewSHA)
	})

	t.Run("changed profile entry → Modified with profile kind", func(t *testing.T) {
		prev := mkLock(nil)
		prev.Profiles["https://github.com/o/r@profiles/dev"] = remote.LockEntry{SHA: "p11"}
		curr := mkLock(nil)
		curr.Profiles["https://github.com/o/r@profiles/dev"] = remote.LockEntry{SHA: "p22"}
		cs := DiffLockfiles(prev, curr, nil)
		assert.Empty(t, cs.Added)
		require.Len(t, cs.Modified, 1)
		assert.Equal(t, remote.ItemTypeProfile, cs.Modified[0].Kind)
		assert.Equal(t, "p11", cs.Modified[0].OldSHA)
		assert.Equal(t, "p22", cs.Modified[0].NewSHA)
	})

	t.Run("profile entries respect the trust filter", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		registry, err := remote.NewRegistry("", remote.WithRegistryFS(fs))
		require.NoError(t, err)
		require.NoError(t, registry.Add("trusted", "https://github.com/trusted/repo"))
		require.NoError(t, registry.SetTrustBundles("trusted", true))

		curr := mkLock(nil)
		curr.Profiles["https://github.com/trusted/repo@profiles/dev"] = remote.LockEntry{SHA: "p11"}
		cs := DiffLockfiles(mkLock(nil), curr, registry)
		assert.True(t, cs.IsEmpty(), "trusted remotes auto-apply without review for profiles too")
	})

	t.Run("bundle entries report bundle kind", func(t *testing.T) {
		cs := DiffLockfiles(mkLock(nil), mkLock(map[string]string{"a/x": "111"}), nil)
		require.Len(t, cs.Added, 1)
		assert.Equal(t, remote.ItemTypeBundle, cs.Added[0].Kind)
	})

	t.Run("pin flag on prev does not block new Added entries", func(t *testing.T) {
		// A new bundle (not in prev at all) can't be pinned because
		// there's no active entry yet. The Added path must still fire.
		prev := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
			"alice/security": {SHA: "aaa", Pinned: true},
		}}
		curr := mkLock(map[string]string{
			"alice/security": "aaa", // same SHA → no Modified
			"alice/fresh":    "fff", // new → Added
		})
		cs := DiffLockfiles(prev, curr, nil)
		require.Len(t, cs.Added, 1)
		assert.Equal(t, "alice/fresh", cs.Added[0].Name)
		assert.Empty(t, cs.Modified)
	})
}
