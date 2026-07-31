//go:build !windows

package plans

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestList_UnreadablePlanFileFailsLoudly pins the PER-FILE half of "I could not
// read it must never render as it is not there". The sibling pin covers an
// unreadable session DIRECTORY; this covers a plan file whose directory reads
// fine and whose contents do not.
//
// The failure mode being guarded is not an empty list but a plausible one: a
// swallowed read left the Plan in place with Title quietly falling back to the
// file name and Sessions quietly nil, so a consumer could not tell a plan with
// no frontmatter from a plan nobody could read.
//
// The fixture is a symlink cycle rather than a chmod, so it is hostile
// regardless of the uid running the suite — a permission-based fixture is not
// hostile to root, and a test that skips there is a test that never runs.
func TestList_UnreadablePlanFileFailsLoudly(t *testing.T) {
	root := t.TempDir()
	harpDir := filepath.Join(root, "vital-deaf-stunt")
	require.NoError(t, os.MkdirAll(harpDir, 0o755))

	good := filepath.Join(harpDir, "fine"+paths.PlanFileExt)
	require.NoError(t, os.WriteFile(good, []byte("---\ntitle: Fine\n---\nbody\n"), 0o644))

	loopA := filepath.Join(harpDir, "loop"+paths.PlanFileExt)
	loopB := filepath.Join(harpDir, "other-loop"+paths.PlanFileExt)
	require.NoError(t, os.Symlink(loopB, loopA))
	require.NoError(t, os.Symlink(loopA, loopB))

	// Fixture check: the file really is unreadable, and unreadable for a
	// reason that is NOT "does not exist" — List legitimately skips vanished
	// entries, so an ENOENT fixture would prove nothing.
	_, readErr := os.ReadFile(loopA)
	require.Error(t, readErr, "the fixture plan file is readable; nothing is being tested")
	require.False(t, os.IsNotExist(readErr),
		"the fixture must fail for a reason other than absence, which List deliberately tolerates")

	got, err := List(root)
	require.Error(t, err, "an unreadable plan file must be a loud failure, not a quietly degraded entry")
	assert.ErrorContains(t, err, "loop"+paths.PlanFileExt, "the error must name the plan that could not be read")
	assert.Nil(t, got, "no partial listing may be handed back alongside the failure")
}
