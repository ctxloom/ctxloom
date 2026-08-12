package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// bindHarpForEssence registers one harp for projectDir and returns its name
// plus the path its essence.md would occupy.
func bindHarpForEssence(t *testing.T, projectDir string) (string, string) {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	e, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	p, err := paths.HarpEssencePath(e.HarpName)
	require.NoError(t, err)
	return e.HarpName, p
}

// A READ FAILURE ON AN ESSENCE THAT EXISTS IS NOT "NEVER DISTILLED".
//
// load_session reported every os.ReadFile error — a permissions fault, a
// directory in the essence's place, any I/O error — as "No distilled essence
// for <harp> yet ... run compact_session to generate one". That advice is
// actively wrong for a session that HAS been distilled: the compaction reruns,
// spends an LLM budget, writes to the same unreadable path, and the caller
// loops. The message must name the real fault so the caller stops retrying.
func TestLoadHarpEssence_ReadFailureIsNotReportedAsNeverDistilled(t *testing.T) {
	testsupport.Isolate(t)
	proj := t.TempDir()
	harp, essencePath := bindHarpForEssence(t, proj)

	// A DIRECTORY where the essence file belongs: os.ReadFile fails with
	// EISDIR, which is emphatically not "this was never distilled". Chosen
	// over chmod 0000 because it fails for root too.
	require.NoError(t, os.MkdirAll(essencePath, 0o755))

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	_, out, err := s.loadHarpEssence(harp)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.False(t, out.Loaded, "an unreadable essence is not loaded content")
	assert.NotContains(t, out.Message, "No distilled essence",
		"a read FAILURE must not be reported as a never-distilled session; that sends the caller to re-run a compaction that cannot help")
	assert.NotContains(t, out.Message, "compact_session",
		"re-distilling is the wrong remedy for an essence that exists but cannot be read")
	assert.Contains(t, out.Message, harp, "the diagnostic must name the session it is about")
}

// The genuine never-distilled case must keep its actionable advice: a harp
// with no essence on disk is exactly when "run compact_session" is right.
// This is the other half of the pair — the fix must discriminate, not just
// change the message for everyone.
func TestLoadHarpEssence_MissingEssenceStillAdvisesCompaction(t *testing.T) {
	testsupport.Isolate(t)
	proj := t.TempDir()
	harp, _ := bindHarpForEssence(t, proj)

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(proj, ".ctxloom")})}
	_, out, err := s.loadHarpEssence(harp)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.False(t, out.Loaded)
	assert.Contains(t, out.Message, "No distilled essence",
		"a genuinely undistilled harp must still say so")
	assert.Contains(t, out.Message, "compact_session",
		"and must still name the remedy that works")
}
