package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const strictLockKey = "ctxloom+git://example.test/repo//bundles/kit"

// The lockfile decodes strictly: a field the format does not model is
// refused, naming the field, never dropped. A dropped field may have carried
// a decision (a hold spelled the old way), and a lockfile that loads with it
// silently gone advances what the user froze.
func TestLoad_RefusesAFieldTheLockfileDoesNotModel(t *testing.T) {
	for field, body := range map[string]string{
		"pinned":                "schema_version: 2\nbundles:\n  " + strictLockKey + ":\n    sha: abc\n    pinned: true\n",
		"tree":                  "schema_version: 2\nbundles:\n  " + strictLockKey + ":\n    sha: abc\n    tree: true\n",
		"ctxloom_version":       "schema_version: 2\nbundles:\n  " + strictLockKey + ":\n    sha: abc\n    ctxloom_version: v1\n",
		"retraction_checked_at": "schema_version: 2\nbundles:\n  " + strictLockKey + ":\n    sha: abc\n    retraction_checked_at: 2026-01-01T00:00:00Z\n",
		"signed_version":        "schema_version: 2\nbundles:\n  " + strictLockKey + ":\n    sha: abc\n    signed_version: v1\n",
		"profiles":              "schema_version: 2\nprofiles: {}\n",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := lockWithBody(t, body).Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), field, "the refusal names the field")
			assert.Contains(t, err.Error(), "/proj/.ctxloom/lock.yaml", "and the file")
			assert.Contains(t, err.Error(), "ctxloom deps pull", "and the fix: pull itself refuses this file, so the fix is the delete")
		})
	}
}

// Every pin and hold is looked up by bundle identity, so an entry keyed any
// other way is one no lookup reaches: refused, naming the key and the fix.
func TestLoad_RefusesAKeyThatIsNotABundleIdentity(t *testing.T) {
	_, err := lockWithBody(t, "schema_version: 2\nbundles:\n  "+strictLockKey+":\n    sha: abc\n  https://example.test/repo@bundles/kit:\n    sha: def\n").Load()
	require.ErrorIs(t, err, ErrLockKeyNotIdentity)
	assert.Contains(t, err.Error(), "https://example.test/repo@bundles/kit")
	assert.Contains(t, err.Error(), "ctxloom deps pull")
}
