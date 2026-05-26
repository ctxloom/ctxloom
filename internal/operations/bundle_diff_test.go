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

		prev := mkLock(nil)
		curr := mkLock(map[string]string{
			"trusted/x":   "111",
			"untrusted/y": "222",
		})
		cs := DiffLockfiles(prev, curr, registry)
		require.Len(t, cs.Added, 1)
		assert.Equal(t, "untrusted/y", cs.Added[0].Name, "trusted remote should be filtered out")
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
}
