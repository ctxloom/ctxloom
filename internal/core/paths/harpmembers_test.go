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
		AtTop:         "",
		InTranscripts: TranscriptsDirName,
	}
	for _, m := range HarpMembers {
		dir, ok := dirs[m.Location]
		require.True(t, ok, "%s: location %d is not one this test knows — extend the map with the new constant", m.Name, m.Location)
		assert.Equal(t, path.Join(dir, m.Name), m.Rel())
	}
}

// TestHarpMembers_MountedRowsAreWhatAContainerWritesOrReads pins the set: a
// containerized run's mail (spool), its claim-checked launch package, the
// canonical transcript its runner records and the context series its
// statusline hook appends. Native history is mounted too, but beside the
// engine homes rather than at its own relative path, so it is not a
// Mounted row.
func TestHarpMembers_MountedRowsAreWhatAContainerWritesOrReads(t *testing.T) {
	var names []string
	for _, m := range MountedMembers() {
		names = append(names, m.Name)
		assert.Equal(t, AtTop, m.Location, "%s: a mount is a top-level member", m.Name)
		assert.Equal(t, Persist, m.Lifetime, "%s: what a container writes outlives the run", m.Name)
	}
	assert.ElementsMatch(t, []string{ContextMetricsFileName, SpoolDirName, PackageDirName, TranscriptsDirName}, names)
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
	got, ok := ClassifyMember(path.Join(WorkDirName, "ctxloom-wt-agent"))
	require.True(t, ok, "a pattern-named entry under work/ (a checkout) is work's")
	assert.Equal(t, WorkDirName, got.Name)
}

// TestHarpMembers_IdentityMemberIsTheSidecarAtTop: the session-dir predicate
// derives from the ONE identity row — the sidecar, at the top of the dir.
func TestHarpMembers_IdentityMemberIsTheSidecarAtTop(t *testing.T) {
	id := IdentityMember()
	assert.Equal(t, SessionSidecarFileName, id.Name)
	assert.Equal(t, AtTop, id.Location)
	assert.Equal(t, MemberIdentity, id.Tier)
}
