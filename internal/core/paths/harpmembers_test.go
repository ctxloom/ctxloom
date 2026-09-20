package paths

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHarpMembers_EveryRowIsFullyClassified: the table is the ONE
// classification every walker, reaper and mount list derives from, so a row
// with a zero column would make some consumer's answer "unknown" — every row
// names a member, a tier, a location and a lifetime, and no name repeats
// within its location.
func TestHarpMembers_EveryRowIsFullyClassified(t *testing.T) {
	require.NotEmpty(t, HarpMembers)
	seen := map[string]bool{}
	for _, m := range HarpMembers {
		assert.NotEmpty(t, m.Name, "a row with no name classifies nothing")
		assert.NotZero(t, m.Tier, "%s: tier unset", m.Name)
		assert.NotZero(t, m.Location, "%s: location unset", m.Name)
		assert.NotZero(t, m.Lifetime, "%s: lifetime unset", m.Name)
		rel := m.Rel()
		assert.False(t, seen[rel], "%s appears twice in the table", rel)
		seen[rel] = true
	}
}

// TestHarpMembers_RelIsTheLocationDirJoinedWithTheName pins Rel against the
// location constants themselves, so a row's path cannot drift from the
// directory its Location names.
func TestHarpMembers_RelIsTheLocationDirJoinedWithTheName(t *testing.T) {
	dirs := map[MemberLocation]string{
		AtTop:       "",
		InPersist:   PersistDirName,
		InSegments:  SegmentsDirName,
		InEphemeral: EphemeralDirName,
		InHome:      SessionHomeDirName,
	}
	for _, m := range HarpMembers {
		dir, ok := dirs[m.Location]
		require.True(t, ok, "%s: location %d is not one this test knows — extend the map with the new constant", m.Name, m.Location)
		assert.Equal(t, path.Join(dir, m.Name), m.Rel())
	}
}

// TestHarpMembers_ExactlyTheSpoolRowIsMounted: container mail rides the
// session-state mount, and the mount list is DERIVED from this column — the
// spool row is the one member a containerized run must be able to reach, so
// it is the one row marked, and it lives under persist/ (what the mount
// carries).
func TestHarpMembers_ExactlyTheSpoolRowIsMounted(t *testing.T) {
	var mounted []HarpMember
	for _, m := range HarpMembers {
		if m.Mounted {
			mounted = append(mounted, m)
		}
	}
	require.Len(t, mounted, 1, "exactly one row is Mounted")
	assert.Equal(t, SpoolDirName, mounted[0].Name)
	assert.Equal(t, InPersist, mounted[0].Location)
	assert.Equal(t, Persist, mounted[0].Lifetime, "the spool persists: mail outlives the workspace")
}

// TestMountedLocations_AreTheLocationDirsOfTheMountedRows: the container's
// session-state mounts are the LOCATION directories of the Mounted rows
// (a member under persist/ is reached by mounting persist/), each named
// once.
func TestMountedLocations_AreTheLocationDirsOfTheMountedRows(t *testing.T) {
	got := MountedLocations()
	assert.Equal(t, []string{PersistDirName}, got)
}

// TestClassifyMember_ResolvesEveryRowAndWhatLiesBeneathIt: a path relative
// to the session dir classifies to the member it is or the DEEPEST member it
// lives under (a nested row beats the directory holding it); a top-level
// name no row carries is not a member.
func TestClassifyMember_ResolvesEveryRowAndWhatLiesBeneathIt(t *testing.T) {
	for _, m := range HarpMembers {
		got, ok := ClassifyMember(m.Rel())
		require.True(t, ok, "%s: the row's own path must classify", m.Rel())
		assert.Equal(t, m, got)

		got, ok = ClassifyMember(path.Join(m.Rel(), "deeper", "leaf"))
		require.True(t, ok, "%s: a descendant classifies to the member it lives under", m.Rel())
		assert.Equal(t, m, got)
	}
	_, ok := ClassifyMember("")
	assert.False(t, ok, "the session dir itself is not a member")
	_, ok = ClassifyMember("not-a-member")
	assert.False(t, ok)
	got, ok := ClassifyMember(path.Join(PersistDirName, "plan.plan.md"))
	require.True(t, ok, "a pattern-named file under persist/ (a plan file) is persist's")
	assert.Equal(t, PersistDirName, got.Name)
}

// TestHarpMembers_IdentityMemberIsTheSidecarAtTop: the session-dir predicate
// derives from the ONE identity row — the sidecar, at the top of the dir.
func TestHarpMembers_IdentityMemberIsTheSidecarAtTop(t *testing.T) {
	id := IdentityMember()
	assert.Equal(t, SessionSidecarFileName, id.Name)
	assert.Equal(t, AtTop, id.Location)
	assert.Equal(t, MemberIdentity, id.Tier)
}
