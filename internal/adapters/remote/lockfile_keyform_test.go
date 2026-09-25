package remote

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lockWithBody(t *testing.T, body string) *LockfileManager {
	t.Helper()
	fs := afero.NewMemMapFs()
	lm := NewLockfileManager("/proj/.ctxloom", WithLockfileFS(fs))
	require.NoError(t, afero.WriteFile(fs, lm.Path(), []byte(body), 0o644))
	return lm
}

// A lockfile written before keys became bundle identities is REFUSED, not
// read: its keys spell a repository the way the user typed it, the trust gate
// now looks retractions up by identity, and an entry that no lookup reaches is
// a retraction silently not enforced. The refusal names the fix and lists the
// held entries, because a hold is a decision re-pulling does not remember.
func TestLoad_RefusesARetiredKeyForm(t *testing.T) {
	lm := lockWithBody(t, `version: 1
bundles:
  https://example.test/repo@bundles/kit:
    sha: abc123
    url: https://example.test/repo
    held: true
  file:///srv/other@bundles/tools:
    sha: def456
    url: file:///srv/other
`)
	_, err := lm.Load()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLockKeyFormRetired), "refused with the sentinel, got %v", err)
	assert.Contains(t, err.Error(), "ctxloom deps pull", "the refusal names the fix")
	assert.Contains(t, err.Error(), "https://example.test/repo@bundles/kit", "the refusal lists the held entry")
	assert.NotContains(t, err.Error(), "file:///srv/other@bundles/tools", "only held entries need re-holding")
}

// The version is not the only guard: a current-version file carrying a key
// that is not its own bundle identity (hand-edited, or merged from an old
// branch) is refused the same way, never read with that entry unreachable.
func TestLoad_RefusesANonIdentityKeyAtTheCurrentVersion(t *testing.T) {
	lm := lockWithBody(t, `version: 2
bundles:
  ctxloom+git://example.test/repo//bundles/kit:
    sha: abc123
  https://Example.TEST/repo@bundles/kit:
    sha: def456
    held: true
`)
	_, err := lm.Load()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLockKeyFormRetired), "refused with the sentinel, got %v", err)
	assert.Contains(t, err.Error(), "https://Example.TEST/repo@bundles/kit")
}

// A current lockfile keyed by identity loads, and Save stamps the current
// version so a round trip never produces a file the next Load refuses.
func TestLoad_AcceptsIdentityKeysAndSaveStampsTheVersion(t *testing.T) {
	lm := lockWithBody(t, `version: 2
bundles:
  ctxloom+git://example.test/repo//bundles/kit:
    sha: abc123
    held: true
`)
	lock, err := lm.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(ItemTypeBundle, "ctxloom+git://example.test/repo//bundles/kit")
	require.True(t, ok)
	assert.True(t, entry.Held)

	lock.Version = 0
	require.NoError(t, lm.Save(lock))
	again, err := lm.Load()
	require.NoError(t, err)
	assert.Equal(t, LockfileVersion, again.Version)
}
