package antigravity

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Characterization pins for the collapse of this
// package's hand-rolled scan/stream loop onto vendorreader.ConvertJSONLLines (the
// shared shell codex and claude already delegate to).
//
// These cannot be red. The two implementations are behaviourally EQUIVALENT —
// same ctx check in the same place, same "no Session event", same in-order
// dispatch — so a parity test between them has nothing to disagree about.
// Their job is instead to pin the
// SHARED behaviour before the collapse so the collapse is provably
// behaviour-preserving, and pin the parts of it the existing fixture/golden
// tests do NOT reach.

// recordFailsAt is a Recorder whose Nth Record call fails. Used to pin that a
// sink failure is FATAL and propagates verbatim — the one half of the
// VendorAdapter contract ("a rec.Record failure IS fatal") that the fixture
// tests never exercise, since a real Recorder never refuses.
type recordFailsAt struct {
	n     int
	calls int
	err   error
}

func (r *recordFailsAt) Record(agent.ChatEvent) error {
	r.calls++
	if r.calls == r.n {
		return r.err
	}
	return nil
}
func (r *recordFailsAt) Close() error { return nil }

var _ transcript.Recorder = (*recordFailsAt)(nil)

func TestConvertLines_RecordFailureIsFatalAndPropagates(t *testing.T) {
	testsupport.Isolate(t)
	boom := errors.New("sink refused")
	rec := &recordFailsAt{n: 2, err: boom}

	src := writeLines(t, "record-fails.jsonl",
		`{"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>one</USER_REQUEST>"}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"DONE","content":"two"}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"DONE","content":"three"}`+"\n")
	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)

	err := convertLines(context.Background(), rec, lines)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 2, rec.calls, "conversion must stop at the failing Record, not run the file out")
}

// countingRecorder records nothing but counts, so a cancellation pin can say
// exactly how many lines were processed before the stream stopped.
type countingRecorder struct{ calls int }

func (c *countingRecorder) Record(agent.ChatEvent) error { c.calls++; return nil }
func (c *countingRecorder) Close() error                 { return nil }

// TestConvertLines_CancellationIsCheckedBeforeEachLine pins WHERE the ctx
// check sits: before a line is processed, not after the loop. A cancelled ctx
// must therefore stop the stream having recorded nothing at all, and must
// return ctx's own error rather than one of the zero-entry floor diagnostics —
// a cancelled import is not a drifted vendor format.
func TestConvertLines_CancellationIsCheckedBeforeEachLine(t *testing.T) {
	testsupport.Isolate(t)
	rec := &countingRecorder{}
	src := writeLines(t, "cancel.jsonl",
		`{"type":"USER_INPUT","status":"DONE","content":"<USER_REQUEST>one</USER_REQUEST>"}`+"\n"+
			`{"type":"PLANNER_RESPONSE","status":"DONE","content":"two"}`+"\n")
	lines, lerr := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	require.NoError(t, lerr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := convertLines(ctx, rec, lines)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, rec.calls, "nothing may be recorded once the context is already done")
}

// TestConvertLines_EmptyInputIsASilentSuccess pins the boundary the collapse
// must not move: zero lines is not a drifted format and not a failure. It is
// also the case where this project's signature bug hides — the Recorder
// creates no file until the first successful Record, so "no lines" and "wrote
// nothing" look identical from outside.
func TestConvertLines_EmptyInputIsASilentSuccess(t *testing.T) {
	testsupport.Isolate(t)
	rec := &countingRecorder{}
	assert.NoError(t, convertLines(context.Background(), rec, nil))
	assert.Zero(t, rec.calls)
}
