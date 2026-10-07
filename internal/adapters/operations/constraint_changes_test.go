package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// constraintChanges names only a pin whose declared constraint moved. Each
// other ref, a malformed one included, is skipped for its own reason, and a
// skipped ref never ends the walk: the moved one is declared LAST.
func TestConstraintChanges_NamesOnlyAMovedConstraint(t *testing.T) {
	tmp := t.TempDir()
	const repo = "https://github.com/test/repo@bundles/"
	const sha = "0123456789abcdef0123456789abcdef01234567"
	registerTestRemote(t, tmp, "https://github.com/test/repo")
	unparseable := "https://github.com/test/repo"  // never collected: it names no bundle
	unkeyable := "https://github.com/@bundles/x" // parses, but names no repository
	_, err := remote.ParseReference(unparseable)
	require.Error(t, err)
	parsed, err := remote.ParseReference(unkeyable)
	require.NoError(t, err)
	_, err = parsed.LockKey()
	require.Error(t, err)
	refs := []string{
		unparseable,
		unkeyable,
		repo + "unpinned@^1.0",   // no pin yet: pull's job, not a change
		repo + "held@^1.0",       // held: the user froze it
		repo + "same@^1.0",       // pinned from the constraint still declared
		repo + "commit@" + sha,   // a bare commit the pin already sits at
		repo + "moved@^2.0",      // the one change
	}
	writeLocalProfile(t, tmp, "dev", "bundles:\n  - "+strings.Join(refs, "\n  - ")+"\n")
	lock := &remote.Lockfile{Bundles: map[ident.BundleKey]remote.LockEntry{
		lockKeyOf(t, refs[3]): {SHA: sha, RequestedVersion: "^0.9", Held: true},
		lockKeyOf(t, refs[4]): {SHA: sha, RequestedVersion: "^1.0"},
		lockKeyOf(t, refs[5]): {SHA: sha, RequestedVersion: "^3.0"},
		lockKeyOf(t, refs[6]): {SHA: sha, RequestedVersion: "^1.0"},
	}}

	got := constraintChanges(testConfigWithSCMPath(tmp), []string{"dev"}, lock)

	assert.Equal(t, []ConstraintChange{{Identity: string(lockKeyOf(t, refs[6])), Pinned: "^1.0", Declared: "^2.0", SHA: sha}}, got)
}
