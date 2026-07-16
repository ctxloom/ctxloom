package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// TestTranscriptSize_PrefersCanonicalOverLegacy pins the tough-cloud S4
// staleness-fingerprint fix: once a harp has a captured canonical transcript,
// that is the file Compact actually distills from (NewCompactor wraps the
// production source in pb.CanonicalFallbackSource), so the size fingerprint
// stamped into the essence — and later compared by Entry.SourceStale — must
// be the CANONICAL file's size, not the legacy engine file's. The legacy and
// canonical fixtures here are deliberately different, known sizes so a size
// match proves which file was actually stat'd.
func TestTranscriptSize_PrefersCanonicalOverLegacy(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "codex")
	require.NoError(t, err)

	legacyPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte("0123456789"), 0o644)) // exactly 10 bytes
	require.NoError(t, mgr.BindSession(entry.HarpName, "backend-uuid", legacyPath))

	rec, err := transcript.NewRecorder(entry.HarpName, "codex")
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{
		Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "canonical payload, deliberately longer than 10 bytes"},
	}))
	require.NoError(t, rec.Close())

	canonicalPath, err := paths.HarpCanonicalTranscriptPath(entry.HarpName)
	require.NoError(t, err)
	info, err := os.Stat(canonicalPath)
	require.NoError(t, err)

	got := transcriptSize(entry.HarpName)
	assert.Equal(t, info.Size(), got, "must stat the canonical file's real size")
	assert.NotEqual(t, int64(10), got, "must NOT have stat'd the legacy 10-byte file once canonical exists")
}

// TestTranscriptSize_FallsBackToLegacyWhenNoCanonical proves the transitional
// half: a harp with no canonical transcript (predates capture) still
// fingerprints against its legacy TranscriptPath, unchanged from pre-S4
// behavior.
func TestTranscriptSize_FallsBackToLegacyWhenNoCanonical(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/proj", "codex")
	require.NoError(t, err)

	legacyPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte("0123456789"), 0o644)) // exactly 10 bytes
	require.NoError(t, mgr.BindSession(entry.HarpName, "backend-uuid", legacyPath))
	// Deliberately no canonical transcript written for this harp.

	assert.Equal(t, int64(10), transcriptSize(entry.HarpName))
}
