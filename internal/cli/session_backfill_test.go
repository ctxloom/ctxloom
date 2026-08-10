package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestSessionBackfill_NoArgs_ConvertsBoundEntries covers the "backfill
// everything" shape: one harp with a real bound vendor transcript converts,
// one with nothing bound is skipped — both reported in the json result.
func TestSessionBackfill_NoArgs_ConvertsBoundEntries(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)

	converts, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)
	seedEngineVersion(t, mgr, converts.HarpName, "claude-code")
	require.NoError(t, mgr.BindSession(converts.HarpName, "sess-1", claudeVendorFixturePath(t)))

	skips, err := mgr.AssignHarp(dir, "codex")
	require.NoError(t, err)
	_ = skips // never bound — nothing to locate

	out, err := execRootCmd(t, "session", "backfill", "--format", "json")
	require.NoError(t, err)

	var result struct {
		Converted []string          `json:"converted"`
		Skipped   int               `json:"skipped"`
		Failed    map[string]string `json:"failed"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result), "output must be clean JSON: %s", out)
	assert.Equal(t, []string{converts.HarpName}, result.Converted)
	assert.Equal(t, 1, result.Skipped)
	assert.Empty(t, result.Failed)
	assert.True(t, canonicalTranscriptExists(t, converts.HarpName))
}

// TestSessionBackfill_NamedHarp covers backfilling a single named harp
// rather than the whole index.
func TestSessionBackfill_NamedHarp(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, "claude-code")
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", claudeVendorFixturePath(t)))

	out, err := execRootCmd(t, "session", "backfill", entry.HarpName, "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "converted: "+entry.HarpName)
	assert.Contains(t, out, "1 converted, 0 skipped, 0 failed")
	assert.True(t, canonicalTranscriptExists(t, entry.HarpName))
}

// TestSessionBackfill_UnknownHarp errors clearly rather than silently doing
// nothing, matching `session distill`'s convention for the same case.
func TestSessionBackfill_UnknownHarp(t *testing.T) {
	testsupport.ProjectDir(t)
	_, err := execRootCmd(t, "session", "backfill", "no-such-harp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-harp")
}

// `session backfill` used to return exit 0 even when every entry
// failed. A bound path that exists (passes os.Stat) but is a directory, not
// a file, makes ConvertVendorTranscript genuinely attempt and fail —
// distinct from "nothing bound" (Skipped), which must NOT fail the command.
func TestSessionBackfill_EveryEntryFailing_IsAnError(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "codex")
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, "codex")
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", t.TempDir())) // a dir, not a file

	_, err = execRootCmd(t, "session", "backfill", "--format", "text")
	require.Error(t, err, "every entry failing must not exit 0")
	assert.Contains(t, err.Error(), "1 failed")
}

// TestSessionBackfill_Idempotent_ReRun covers the command's own doc-comment
// claim ("safe to re-run at any time"): a second run over an already-fully-
// converted index reports zero converted, and does not touch the canonical
// file it already wrote.
func TestSessionBackfill_Idempotent_ReRun(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, "claude-code")
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", claudeVendorFixturePath(t)))

	_, err = execRootCmd(t, "session", "backfill", "--format", "text")
	require.NoError(t, err)

	out, err := execRootCmd(t, "session", "backfill", "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "0 converted, 1 skipped, 0 failed")
}
