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
func TestSessionEssenceInfo_HarpDirWinsOverLegacy(t *testing.T) {
	testsupport.Isolate(t)
	harp := "plump-loose-sash"
	appDir := t.TempDir()

	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	harpPath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(harpPath, []byte("harp-dir body\n"), 0o644))

	legacyDir := paths.ProjectSessionsDir(appDir)
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "sess-1.md"), []byte("legacy body\n"), 0o644))

	e := sessions.Entry{HarpName: harp, SessionID: "sess-1"}
	gotPath, distilled := SessionEssenceInfo(harp, &e, appDir)

	assert.True(t, distilled)
	assert.Equal(t, harpPath, gotPath, "harp-dir layout wins over the legacy fallback")
}

func TestSessionEssenceInfo_FallsBackToLegacyWhenNoHarpEssence(t *testing.T) {
	testsupport.Isolate(t)
	appDir := t.TempDir()
	legacyDir := paths.ProjectSessionsDir(appDir)
	require.NoError(t, os.MkdirAll(legacyDir, 0o755))
	legacyPath := filepath.Join(legacyDir, "sess-2.md")
	require.NoError(t, os.WriteFile(legacyPath, []byte("legacy body\n"), 0o644))

	e := sessions.Entry{HarpName: "swift-amber-falcon", SessionID: "sess-2"}
	gotPath, distilled := SessionEssenceInfo("swift-amber-falcon", &e, appDir)

	assert.True(t, distilled)
	assert.Equal(t, legacyPath, gotPath)
}

func TestSessionEssenceInfo_NeitherPresentIsNotDistilled(t *testing.T) {
	testsupport.Isolate(t)
	appDir := t.TempDir()
	e := sessions.Entry{HarpName: "never-distilled-harp", SessionID: "sess-3"}

	gotPath, distilled := SessionEssenceInfo("never-distilled-harp", &e, appDir)

	assert.False(t, distilled)
	assert.Empty(t, gotPath)
}

// TestSessionEssenceInfo_DirectoryAtEssencePathIsNotDistilled pins the
// directory-exclusion the inlined os.Stat check preserves from cli's former
// fileExists helper: a DIRECTORY sitting at either candidate path must not
// read as a distilled essence. This is the one deliberate divergence from
// the near-identical checks in internal/lm/isolation and internal/codex (see
// SessionEssenceInfo's doc) — it must not be lost by future refactoring.
func TestSessionEssenceInfo_DirectoryAtEssencePathIsNotDistilled(t *testing.T) {
	testsupport.Isolate(t)
	harp := "dir-at-essence-path"
	appDir := t.TempDir()

	harpDir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	harpPath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpPath, 0o755), "a directory, not a file, at the harp-dir essence path")

	e := sessions.Entry{HarpName: harp, SessionID: "sess-4"}
	gotPath, distilled := SessionEssenceInfo(harp, &e, appDir)

	assert.False(t, distilled, "a directory at the essence path is not a distilled essence")
	assert.Empty(t, gotPath)
}
