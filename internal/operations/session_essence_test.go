package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionEssenceInfo_HarpDirWinsOverLegacy pins the ONE two-step
// resolution order every essence entry point owes the user — harp-dir layout
// (~/.ctxloom/sessions/<harp>/essence.md) first, legacy
// <appDir>/sessions/<sessionID>.md second. cli's readSessionEssence (the
// READING face) and this function must agree on which of the two candidate
// files wins, or the same session reads as distilled in one command and
// pending in another — see cli's TestSessionEssenceResolution_SharedLookupOrder
// for the cross-package half of that contract.
// seedRotationEssence writes ~/.ctxloom/sessions/<harp>/segments/<id>.md and
// returns its path.
func seedRotationEssence(t *testing.T, harp, sessionID, body string) string {
	t.Helper()
	p, err := paths.ResolveHarpSegmentEssencePath(harp, sessionID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestSessionEssenceInfo_CurrentEssenceWinsOverTheRotationCopy(t *testing.T) {
	testsupport.Isolate(t)
	harp := "plump-loose-sash"

	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	harpPath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(harpPath, []byte("harp-dir body\n"), 0o644))

	rotationPath := seedRotationEssence(t, harp, "sess-1", "rotation body\n")

	e := sessions.Entry{HarpName: harp, SessionID: "sess-1"}
	gotPath, distilled := SessionEssenceInfo(harp, &e)

	assert.True(t, distilled)
	assert.Equal(t, harpPath, gotPath,
		"the harp's CURRENT essence wins: a rotation copy is the record of an older session, not this harp's latest")
	assert.NotEqual(t, rotationPath, gotPath)
}

// A harp whose essence.md has not been written yet — or a session whose harp has
// since been distilled again — is still distilled if THIS rotation left its own
// copy under segments/. Without this arm a /clear would make every earlier
// session in the lineage read as never-distilled.
func TestSessionEssenceInfo_FallsBackToTheRotationEssence(t *testing.T) {
	testsupport.Isolate(t)
	harp := "swift-amber-falcon"
	rotationPath := seedRotationEssence(t, harp, "sess-2", "rotation body\n")

	e := sessions.Entry{HarpName: harp, SessionID: "sess-2"}
	gotPath, distilled := SessionEssenceInfo(harp, &e)

	assert.True(t, distilled)
	assert.Equal(t, rotationPath, gotPath)
}

func TestSessionEssenceInfo_NeitherPresentIsNotDistilled(t *testing.T) {
	testsupport.Isolate(t)
	e := sessions.Entry{HarpName: "never-distilled-harp", SessionID: "sess-3"}

	gotPath, distilled := SessionEssenceInfo("never-distilled-harp", &e)

	assert.False(t, distilled)
	assert.Empty(t, gotPath)
}

// TestSessionEssenceInfo_DirectoryAtEssencePathIsNotDistilled pins the
// directory-exclusion the inlined os.Stat check preserves from cli's former
// fileExists helper: a DIRECTORY sitting at a candidate path must not
// read as a distilled essence. This is the one deliberate divergence from
// the near-identical checks in internal/lm/isolation and internal/codex (see
// SessionEssenceInfo's doc) — it must not be lost by future refactoring.
func TestSessionEssenceInfo_DirectoryAtEssencePathIsNotDistilled(t *testing.T) {
	testsupport.Isolate(t)
	harp := "dir-at-essence-path"

	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	harpPath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpPath, 0o755), "a directory, not a file, at the harp-dir essence path")

	e := sessions.Entry{HarpName: harp, SessionID: "sess-4"}
	gotPath, distilled := SessionEssenceInfo(harp, &e)

	assert.False(t, distilled, "a directory at the essence path is not a distilled essence")
	assert.Empty(t, gotPath)
}
