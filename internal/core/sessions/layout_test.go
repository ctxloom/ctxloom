package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLayout_DirIsTheHarpUnderTheSessionsRoot: one harp-keyed tree under the
// ctxloom home; the project tree holds no session state.
func TestLayout_DirIsTheHarpUnderTheSessionsRoot(t *testing.T) {
	l := Layout{Root: "/h/.ctxloom"}
	assert.Equal(t, filepath.Join("/h/.ctxloom", paths.SessionsDir), l.SessionsRoot())
	assert.Equal(t, filepath.Join("/h/.ctxloom", paths.SessionsDir, "brisk-teal-otter"), l.Dir("brisk-teal-otter"))
}

// TestLayout_MemberDerivesEveryPathFromTheTable walks paths.HarpMembers: a
// member's path is the session dir joined with the row's Rel, for every row,
// so a member added to the table is addressable with no edit here.
func TestLayout_MemberDerivesEveryPathFromTheTable(t *testing.T) {
	l := Layout{Root: "/h/.ctxloom"}
	for _, m := range paths.HarpMembers {
		want := filepath.Join(l.Dir("brisk-teal-otter"), filepath.FromSlash(m.Rel()))
		assert.Equal(t, want, l.Member("brisk-teal-otter", m), m.Rel())
	}
}

// TestLayout_NamedPathsAreTableRows: each named accessor is a table row
// spelled by role, never a second path join.
func TestLayout_NamedPathsAreTableRows(t *testing.T) {
	l := Layout{Root: "/h/.ctxloom"}
	const h = "brisk-teal-otter"
	row := func(name string) paths.HarpMember {
		for _, m := range paths.HarpMembers {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("no table row named %q", name)
		return paths.HarpMember{}
	}
	assert.Equal(t, l.Member(h, row(paths.SessionEngineHomesDirName)), l.SessionEngineHomes(h))
	assert.Equal(t, l.Member(h, row(paths.PersistDirName)), l.Persist(h))
	assert.Equal(t, l.Member(h, row(paths.EphemeralDirName)), l.Ephemeral(h))
	assert.Equal(t, l.Member(h, row(paths.SegmentsDirName)), l.Segments(h))
	assert.Equal(t, l.Member(h, row(paths.SpoolDirName)), l.Spool(h))
	assert.Equal(t, l.Member(h, paths.IdentityMember()), l.Sidecar(h))

	assert.Equal(t, filepath.Join(l.Persist(h), paths.SpoolDirName), l.Spool(h),
		"the spool stays under persist/: that directory is what the container's session-state mount carries")
	assert.Equal(t, filepath.Join(l.Dir(h), paths.SessionEngineHomesDirName), l.SessionEngineHomes(h),
		"the session engine homes dir is a member of the session dir, under the ctxloom home")
}

// TestHomeLayout_AgreesWithTheHarpHelpers: the home-resolving helpers in
// paths and the Layout over the resolved home are ONE tree; a divergence
// here is two answers to where a session lives.
func TestHomeLayout_AgreesWithTheHarpHelpers(t *testing.T) {
	testsupport.Isolate(t)
	l, err := HomeLayout()
	require.NoError(t, err)
	const h = "brisk-teal-otter"

	dir, err := paths.HarpDir(h)
	require.NoError(t, err)
	assert.Equal(t, dir, l.Dir(h))
	persist, err := paths.HarpPersistDir(h)
	require.NoError(t, err)
	assert.Equal(t, persist, l.Persist(h))
	eph, err := paths.HarpEphemeralDir(h)
	require.NoError(t, err)
	assert.Equal(t, eph, l.Ephemeral(h))
	seg, err := paths.ResolveHarpSegmentsDir(h)
	require.NoError(t, err)
	assert.Equal(t, seg, l.Segments(h))
	sidecar, err := paths.HarpSidecarPath(h)
	require.NoError(t, err)
	assert.Equal(t, sidecar, l.Sidecar(h))
}

// TestIsSessionDir_ReadsTheIdentityRow: the session-dir predicate is
// derived from the table's identity member — a directory carrying it is a
// session, one without it is not.
func TestIsSessionDir_ReadsTheIdentityRow(t *testing.T) {
	root := t.TempDir()
	l := Layout{Root: root}
	require.NoError(t, os.MkdirAll(l.Dir("brisk-teal-otter"), 0o755))
	require.NoError(t, os.MkdirAll(l.Dir("calm-red-fox"), 0o755))
	require.NoError(t, os.WriteFile(l.Sidecar("brisk-teal-otter"), []byte("engine: mock\n"), 0o600))

	entries, err := os.ReadDir(l.SessionsRoot())
	require.NoError(t, err)
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = IsSessionDir(l.SessionsRoot(), e)
	}
	assert.Equal(t, map[string]bool{"brisk-teal-otter": true, "calm-red-fox": false}, got)
}
