package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestHomePathFor_LandsUnderHomeLocks pins the shape ruled 2026-08-13
// (closing undated-bronco / fs-consolidation N1): a lock guarding a FOREIGN
// file — one outside any project .ctxloom tree, incl. inside the user's real
// engine homes — lives under ~/.ctxloom/locks, never beside the file itself.
// Beside-the-file (PathFor) is exactly what left `.mcp.json.lock` and
// `~/.claude/settings.json.lock` as untracked litter.
func TestHomePathFor_LandsUnderHomeLocks(t *testing.T) {
	home := testsupport.Isolate(t)
	protected := filepath.Join(home, "project", ".claude", "settings.json")

	got, err := HomePathFor(protected)
	require.NoError(t, err)

	wantDir := filepath.Join(home, ".ctxloom", "locks")
	assert.Equal(t, wantDir, filepath.Dir(got),
		"the lock must sit in the home locks directory, not beside the protected file")
	assert.NotEqual(t, PathFor(protected), got,
		"the home mapping must not collapse back to the beside-the-file shape")
}

// TestHomePathFor_FlattensDistinctPathsToDistinctNames is the mutation-kill
// test for the flatten step: MUTATION — replace flattenLockName's call
// inside HomePathFor with the bare absolute path (or drop the flatten
// entirely) — turns this red, because the lock for a NESTED protected file
// would then land in a directory that mirrors the real tree instead of
// beside its sibling in ONE flat locks directory, and the two distinct
// targets below would no longer be guaranteed to differ only in their
// (single-component) basename.
func TestHomePathFor_FlattensDistinctPathsToDistinctNames(t *testing.T) {
	home := testsupport.Isolate(t)

	nested, err := HomePathFor(filepath.Join(home, "proj", "a", ".claude", "settings.json"))
	require.NoError(t, err)
	other, err := HomePathFor(filepath.Join(home, "proj", "b", ".mcp.json"))
	require.NoError(t, err)

	wantDir := filepath.Join(home, ".ctxloom", "locks")
	assert.Equal(t, wantDir, filepath.Dir(nested),
		"flattening must keep every home lock in ONE directory; a lock tree mirroring the real one is a second convention")
	assert.Equal(t, wantDir, filepath.Dir(other))
	assert.NotEqual(t, nested, other,
		"two distinct protected files must not be handed the same lock by accident")
}

// TestHomePathFor_SpellingsOfOneFileMapToOneLock covers the same invariant
// ProjectPathFor's identically-named test covers for the project case: every
// spelling a caller can plausibly hold of one file — with a dot segment, a
// dot-dot round trip, doubled separators — must resolve to the SAME lock, or
// two writers of the identical file silently fail to exclude each other.
func TestHomePathFor_SpellingsOfOneFileMapToOneLock(t *testing.T) {
	home := testsupport.Isolate(t)
	target := filepath.Join(home, "proj", ".claude", "settings.json")

	spellings := []string{
		target,
		filepath.Join(home, "proj", ".", ".claude", "settings.json"),
		filepath.Join(home, "proj", "other", "..", ".claude", "settings.json"),
		filepath.Join(home, "proj") + string(filepath.Separator) + string(filepath.Separator) + filepath.Join(".claude", "settings.json"),
	}

	want, err := HomePathFor(target)
	require.NoError(t, err)
	for _, spelling := range spellings {
		got, err := HomePathFor(spelling)
		require.NoError(t, err)
		assert.Equal(t, want, got, "%q and %q name the same file; two lock paths means two writers that do not exclude each other", spelling, target)
	}
}

// TestHomePathFor_CollisionsAfterFlatteningShareOneLock pins the documented
// collision stance (HomePathFor's doc, mirroring ProjectPathFor's): two
// protected paths that differ only in WHERE a path separator falls relative
// to an underscore run flatten to the identical name and therefore share one
// lock. That is the deliberately chosen "collision case" — safe
// over-serialization, never the opposite failure of one resource getting two
// lock names.
func TestHomePathFor_CollisionsAfterFlatteningShareOneLock(t *testing.T) {
	home := testsupport.Isolate(t)

	withSeparator, err := HomePathFor(filepath.Join(home, "a", "b", "c"))
	require.NoError(t, err)
	withUnderscore, err := HomePathFor(filepath.Join(home, "a", "b__c"))
	require.NoError(t, err)

	assert.Equal(t, withSeparator, withUnderscore,
		"a path separator and a literal double-underscore must flatten to the same name — this is the documented, accepted collision, not a bug")
}

// TestHomePathFor_FailsClosedWhenHomeCannotBeResolved pins the home-resolution
// error path: os.UserHomeDir failing must propagate rather than fall back to
// a guessed location, and the failure must name what was being resolved.
func TestHomePathFor_FailsClosedWhenHomeCannotBeResolved(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("HOME", "")

	got, err := HomePathFor("/some/foreign/settings.json")
	require.Error(t, err)
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), "/some/foreign/settings.json",
		"the error must name the protected file the lock was being derived for")
}
