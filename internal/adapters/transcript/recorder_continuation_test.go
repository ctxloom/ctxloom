package transcript

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRecorder_ContinuationExtendsAnotherRecordersLines pins what a resumed
// conversion needs from the writer: the lines it appends must read as the
// continuation of the ones already there. Seq picks up with no gap (a reader
// detects truncation by a discontinuity) and every line carries the session id
// the earlier lines' Session record established, though this recorder never
// sees that record.
func TestRecorder_ContinuationExtendsAnotherRecordersLines(t *testing.T) {
	testsupport.Isolate(t)
	harp := "continuation-harp"

	rec, err := NewRecorder(harp, "claude-code", WithContinuation(7, "sess-abc"))
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "one"}}))
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "two"}}))
	require.NoError(t, rec.Close())

	recs := readRecordedLines(t, harp)
	require.Len(t, recs, 2)
	assert.Equal(t, 7, recs[0].Seq)
	assert.Equal(t, 8, recs[1].Seq)
	assert.Equal(t, "sess-abc", recs[0].SessionID)
	assert.Equal(t, "sess-abc", recs[1].SessionID)
}
