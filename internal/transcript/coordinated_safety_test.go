package transcript

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestCoordinatedRecorder_SubmitAfterLastProducerDoneDoesNotPanic pins the one
// thing this whole package promises it will never do.
//
// Submit sends on cr.ch, and cr.ch is closed the moment the last ProducerDone
// lands, so a Submit that arrives after it panicked with "send on closed
// channel" — raised in the CALLER's goroutine, which at both production call
// sites is the live chat's own inbound pump or outbound tee. Every other
// failure in this package is deliberately swallowed precisely so that
// transcript capture can never perturb the conversation it shadows; a panic
// out of Submit is the maximal violation of that, and it is reachable from
// nothing worse than a producer that miscounts its own lifecycle.
//
// The event is dropped — there is nothing left to write it with — but the drop
// is reported rather than silent.
func TestCoordinatedRecorder_SubmitAfterLastProducerDoneDoesNotPanic(t *testing.T) {
	rec := &closeTrackingRecorder{}
	cr := NewCoordinatedRecorder(rec, 1)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	cr.ProducerDone()
	select {
	case <-cr.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the recorder never closed after its only producer finished")
	}

	assert.NotPanics(t, func() {
		cr.Submit(context.Background(), agent.ChatEvent{
			Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "late"},
		})
	}, "a late Submit must not panic the goroutine that called it")

	assert.Contains(t, sink.String(), "transcript",
		"the dropped event must be reported, not silently discarded")
}

// TestCoordinatedRecorder_ZeroProducersSubmitDoesNotPanic is the same defect
// reached by construction rather than by lifecycle: producers == 0 means
// wg.Wait returns immediately, so the channel is closed before any caller can
// possibly have submitted anything.
func TestCoordinatedRecorder_ZeroProducersSubmitDoesNotPanic(t *testing.T) {
	rec := &closeTrackingRecorder{}
	cr := NewCoordinatedRecorder(rec, 0)

	select {
	case <-cr.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a zero-producer recorder never closed")
	}

	assert.NotPanics(t, func() {
		cr.Submit(context.Background(), agent.ChatEvent{
			Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "orphan"},
		})
	})
}

// TestCoordinatedRecorder_SubmitStillDeliversBeforeClose is the guard on the
// guard: making a late Submit safe must not make an ordinary one lossy. Every
// event submitted while a producer is still live is recorded, and Submit still
// returns only after Record has actually returned.
func TestCoordinatedRecorder_SubmitStillDeliversBeforeClose(t *testing.T) {
	rec := &entryTrackingRecorder{t: t}
	cr := NewCoordinatedRecorder(rec, 1)

	for i := range 5 {
		cr.Submit(context.Background(), agent.ChatEvent{
			Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: string(rune('a' + i))},
		})
	}
	cr.ProducerDone()
	select {
	case <-cr.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("recorder never closed")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.recorded, 5, "no event submitted before the close may be dropped")
}
