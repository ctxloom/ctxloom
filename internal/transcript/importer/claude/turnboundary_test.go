package claude

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// completesFrom runs the adapter over src and returns just the Complete
// boundaries, in order.
func completesFrom(t *testing.T, src string) []*transcript.CompletePayload {
	t.Helper()
	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, src))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)

	var out []*transcript.CompletePayload
	for _, r := range readRecords(t, path) {
		if r.Kind == transcript.KindComplete {
			out = append(out, r.Complete)
		}
	}
	return out
}

// TestConvert_IDlessUsageLineDoesNotSwallowThePendingBoundary pins a real defect.
//
// handleAssistant only flushed the pending turn boundary when the incoming
// message.id was NON-EMPTY. An assistant line carrying `usage` but an empty
// message.id therefore fell straight through to the unconditional
// `c.pending = &agent.TurnMeta{...}` overwrite, and the previous response's
// accounting — a Complete record that had already been fully assembled — was
// discarded with no error and nothing said. The transcript simply comes out
// one turn short.
//
// A missing message.id is not exotic: it is what any drift in claude's own
// assistant-line shape produces, and the field is `omitempty` on the way in.
// Whatever the incoming line's identity is, it is NOT the pending one's, so
// the pending boundary is closed rather than dropped.
func TestConvert_IDlessUsageLineDoesNotSwallowThePendingBoundary(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "idless-usage.jsonl",
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","id":"msg_A","model":"claude-x","stop_reason":"tool_use","content":[{"type":"text","text":"first"}],"usage":{"input_tokens":11,"output_tokens":22}}}`+"\n"+
			`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"claude-x","stop_reason":"end_turn","content":[{"type":"text","text":"second"}],"usage":{"input_tokens":33,"output_tokens":44}}}`+"\n")

	completes := completesFrom(t, src)
	require.Len(t, completes, 2, "msg_A's accounting must be flushed when a line with a different (here: absent) message.id arrives, not silently overwritten")
	assert.Equal(t, 11, completes[0].InputTokens)
	assert.Equal(t, 22, completes[0].OutputTokens)
	assert.Equal(t, "tool_use", completes[0].StopReason)
	assert.Equal(t, 33, completes[1].InputTokens)
	assert.Equal(t, 44, completes[1].OutputTokens)
	assert.Equal(t, "end_turn", completes[1].StopReason)
}

// The mirror case: an id-LESS pending boundary followed by an id'd one. The
// old guard required c.pendingID != "" as well, so this lost the first
// response's accounting for the same reason.
func TestConvert_IDlessPendingIsFlushedWhenAnIDdLineArrives(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "idless-pending.jsonl",
		`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","model":"claude-x","stop_reason":"tool_use","content":[{"type":"text","text":"first"}],"usage":{"input_tokens":11,"output_tokens":22}}}`+"\n"+
			`{"type":"assistant","sessionId":"s1","message":{"role":"assistant","id":"msg_B","model":"claude-x","stop_reason":"end_turn","content":[{"type":"text","text":"second"}],"usage":{"input_tokens":33,"output_tokens":44}}}`+"\n")

	completes := completesFrom(t, src)
	require.Len(t, completes, 2, "an id-less pending boundary is still a boundary; a differently-identified line must close it")
	assert.Equal(t, 11, completes[0].InputTokens)
	assert.Equal(t, 33, completes[1].InputTokens)
}

// Discriminator, so the fix above cannot be satisfied by "flush on every
// usage line": repeated lines of the SAME response carry the identical
// message.id and must still fold into ONE boundary, not one per line. This is
// the behaviour converter's doc comment exists to protect, and the reason the
// guard compares ids at all.
func TestConvert_RepeatedSameIDUsageLinesStillFoldIntoOneBoundary(t *testing.T) {
	testsupport.Isolate(t)
	line := `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","id":"msg_A","model":"claude-x","stop_reason":"end_turn","content":[{"type":"text","text":"chunk"}],"usage":{"input_tokens":11,"output_tokens":22}}}` + "\n"
	src := writeLines(t, "same-id.jsonl", line+line+line)

	completes := completesFrom(t, src)
	require.Len(t, completes, 1, "three lines of one response are one turn, not three")
	assert.Equal(t, 11, completes[0].InputTokens)
}
