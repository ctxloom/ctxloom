package memory

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// TestTranscriptSize_PrefersCanonicalOverLegacy pins the S4
// staleness-fingerprint fix: once a harp has a captured canonical transcript,
// that is the file Compact actually distills from (NewCompactor wraps the
// production source in pb.CanonicalFallbackSource), so the size fingerprint
// stamped into the essence — and later compared by Entry.SourceStale — must
// be the CANONICAL file's size, not the legacy engine file's. The legacy and
// canonical fixtures here are deliberately different, known sizes so a size
// match proves which file was actually stat'd.
func TestTranscriptSize_PrefersCanonicalOverLegacy(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	legacyPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte("0123456789"), 0o644)) // exactly 10 bytes
	require.NoError(t, mgr.BindSession(entry.HarpName, "backend-uuid", legacyPath))

	rec, err := transcript.NewRecorder(entry.HarpName, "claude-code")
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{
		Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "canonical payload, deliberately longer than 10 bytes"},
	}))
	require.NoError(t, rec.Close())

	canonicalPath, err := paths.HarpCanonicalTranscriptPath(entry.HarpName)
	require.NoError(t, err)
	_, err = os.Stat(canonicalPath)
	require.NoError(t, err)

	got := transcriptEntryCount(entry.HarpName)
	assert.Equal(t, 1, got, "must count the CANONICAL file's one entry record")
	assert.NotEqual(t, 0, got, "must NOT have read the legacy file once canonical exists")
}

// TestTranscriptEntryCount_FallsBackToLegacyWhenNoCanonical proves the transitional
// half: a harp with no canonical transcript (predates capture) still
// fingerprints against its legacy TranscriptPath, unchanged from pre-S4
// behavior.
func TestTranscriptEntryCount_FallsBackToLegacyWhenNoCanonical(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	legacyPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte(
		`{"v":1,"harp":"h","engine":"claude-code","seq":0,"ts":"2026-01-01T00:00:00Z","kind":"entry","entry":{"type":"user","content":"one"}}`+"\n"+
			`{"v":1,"harp":"h","engine":"claude-code","seq":1,"ts":"2026-01-01T00:00:01Z","kind":"entry","entry":{"type":"assistant","content":"two"}}`+"\n"), 0o644))
	require.NoError(t, mgr.BindSession(entry.HarpName, "backend-uuid", legacyPath))
	// Deliberately no canonical transcript written for this harp.

	assert.Equal(t, 2, transcriptEntryCount(entry.HarpName))
}

// A harp with a BOUND transcript path that
// can no longer be stat'd (deleted, rotated, permission changed) is a real,
// surprising degradation — unlike "no harp" or "no path bound at all", which
// are ordinary "nothing to fingerprint" cases. The counter silently
// returned 0 for this case with no warning anywhere, so a transient stat
// failure permanently zeroed the staleness fingerprint (disabling the "out
// of date" badge for that harp) with no diagnostic an operator could ever
// see.
func TestTranscriptEntryCount_DanglingBoundPath_Warns(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	gone := filepath.Join(t.TempDir(), "gone.jsonl")
	require.NoError(t, mgr.BindSession(entry.HarpName, "backend-uuid", gone))
	// Deliberately do NOT create `gone` — bound, but unstatable.

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	assert.Equal(t, 0, transcriptEntryCount(entry.HarpName))
	assert.Contains(t, buf.String(), entry.HarpName,
		"a bound-but-unreadable transcript path must warn, naming the harp, instead of silently zeroing the staleness fingerprint")
}
