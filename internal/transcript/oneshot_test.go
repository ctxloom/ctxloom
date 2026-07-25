package transcript

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRecordOneshot_WritesTwoEntryTranscript pins the core S6 contract: a
// non-empty harp + prompt + output writes exactly two lines (user, then
// assistant), readable back with the real payload — not just a line count.
func TestRecordOneshot_WritesTwoEntryTranscript(t *testing.T) {
	testsupport.Isolate(t)
	harp := "oneshot-roundtrip-harp"

	err := RecordOneshot(harp, "kiro", "  what is 2+2?  ", "  4.  ")
	require.NoError(t, err)

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	sess, err := ParseTranscriptFile(path, harp)
	require.NoError(t, err)
	require.Len(t, sess.Entries, 2)

	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Equal(t, "what is 2+2?", sess.Entries[0].Content)
	assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[1].Type)
	assert.Equal(t, "4.", sess.Entries[1].Content)
}

// TestRecordOneshot_NoHarpWritesNothing pins the corrected contract (U144-F15):
// an empty harp still writes no file — there is nothing to key it to — but it
// no longer reports SUCCESS while dropping a real prompt and output on the
// floor. Callers check only the error, so silence there was a lie.
func TestRecordOneshot_NoHarpWritesNothing(t *testing.T) {
	testsupport.Isolate(t)

	err := RecordOneshot("", "codex", "prompt", "output")
	require.Error(t, err)
}

// TestRecordOneshot_EmptyPromptAndOutputWritesNothing pins the empty-input
// discipline (memory "silent-no-op-failure-mode"): a harp with nothing to
// record (both prompt and output blank after trimming) must leave NO file —
// never a zero-byte or blank-entries file a reader could mistake for
// "captured, and genuinely empty" (recorder.go's NewRecorder documents the
// same contract for the structured path; this is its oneshot mirror).
func TestRecordOneshot_EmptyPromptAndOutputWritesNothing(t *testing.T) {
	testsupport.Isolate(t)
	harp := "oneshot-empty-harp"

	err := RecordOneshot(harp, "codex", "   ", "\n\t  ")
	require.NoError(t, err)

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "no transcript should be written for an all-blank prompt+output")
}

// TestRecordOneshot_PromptOnly_WritesOneEntry pins the partial case: an
// empty captured output (a run that produced no stdout, e.g. a failed
// Execute before the engine wrote anything) still records the user's
// prompt rather than dropping the whole transcript.
func TestRecordOneshot_PromptOnly_WritesOneEntry(t *testing.T) {
	testsupport.Isolate(t)
	harp := "oneshot-prompt-only-harp"

	err := RecordOneshot(harp, "antigravity", "do the thing", "")
	require.NoError(t, err)

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	sess, err := ParseTranscriptFile(path, harp)
	require.NoError(t, err)
	require.Len(t, sess.Entries, 1)
	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Equal(t, "do the thing", sess.Entries[0].Content)
}
