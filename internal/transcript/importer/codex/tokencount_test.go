package codex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

func completesFrom(t *testing.T, src string) []*transcript.CompletePayload {
	t.Helper()
	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
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

// TestConvert_LaterTokenCountWithoutContextWindowKeepsTheKnownOne pins that
// codex emits SEVERAL token_count events before the task_complete
// that closes one turn (rollout.go's converter doc comment says so, and the
// shipped fixture has two). handleEventMsg assigned
// c.pending.ContextWindow = p.Info.ModelContextWindow unconditionally, so a
// later event that simply omits model_context_window decoded it as 0 and
// ZEROED a context window the earlier event had already reported.
//
// The result is a Complete record claiming a context window of zero — a
// number no codex build ever emits and that reads downstream as "unknown",
// silently, from data that was in fact known. Absent is not zero; the whole
// SessionInfoBuilder family treats a zero window as "not observed" for
// exactly this reason.
func TestConvert_LaterTokenCountWithoutContextWindowKeepsTheKnownOne(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "window-drop.jsonl",
		`{"timestamp":"2026-07-16T00:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`+"\n"+
			`{"timestamp":"2026-07-16T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10,"cached_input_tokens":1,"output_tokens":2},"model_context_window":258400}}}`+"\n"+
			`{"timestamp":"2026-07-16T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":20,"cached_input_tokens":3,"output_tokens":4}}}}`+"\n"+
			`{"timestamp":"2026-07-16T00:00:03Z","type":"event_msg","payload":{"type":"task_complete","duration_ms":99}}`+"\n")

	completes := completesFrom(t, src)
	require.Len(t, completes, 1)
	assert.Equal(t, 258400, completes[0].ContextWindow,
		"a later token_count that omits model_context_window must not erase the one already reported")
	// The per-turn token fields DO legitimately overwrite — last_token_usage is
	// per-call, and only the last one before task_complete belongs to the
	// boundary (tokenCountPayload's doc comment). Pinned so the fix cannot be
	// mistaken for "never overwrite anything".
	assert.Equal(t, 20, completes[0].InputTokens)
	assert.Equal(t, 4, completes[0].OutputTokens)
	assert.Equal(t, 99, completes[0].DurationMs)
}

// A later token_count that reports a DIFFERENT non-zero window still wins:
// the guard is about absence, not about freezing the first value.
func TestConvert_LaterTokenCountWithANewContextWindowStillWins(t *testing.T) {
	testsupport.Isolate(t)
	src := writeLines(t, "window-change.jsonl",
		`{"timestamp":"2026-07-16T00:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`+"\n"+
			`{"timestamp":"2026-07-16T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10},"model_context_window":258400}}}`+"\n"+
			`{"timestamp":"2026-07-16T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":20},"model_context_window":400000}}}`+"\n"+
			`{"timestamp":"2026-07-16T00:00:03Z","type":"event_msg","payload":{"type":"task_complete","duration_ms":1}}`+"\n")

	completes := completesFrom(t, src)
	require.Len(t, completes, 1)
	assert.Equal(t, 400000, completes[0].ContextWindow)
}
