package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// These tests cover the Transport-2 container oneshot path
// (runOneshotViaCoord): the answer a --one-shot run prints comes off the
// coordinator's event stream, so "we got nothing" and "we got it concurrently"
// are both this function's problem.

const ownedTestRunID = "run-oneshot"

func ownedFinalStart(msgID string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{RunId: ownedTestRunID, Payload: &agentcoordpb.AgentEvent_MessageStarted{
		MessageStarted: &agentcoordpb.MessageStarted{
			MessageId: msgID,
			Role:      agentcoordpb.MessageRole_MESSAGE_ROLE_ASSISTANT,
			Channel:   agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL,
		},
	}}
}

func ownedDelta(msgID, text string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{RunId: ownedTestRunID, Payload: &agentcoordpb.AgentEvent_MessageDelta{
		MessageDelta: &agentcoordpb.MessageDelta{MessageId: msgID, Text: text},
	}}
}

func ownedTurnIdle() *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{RunId: ownedTestRunID, Payload: &agentcoordpb.AgentEvent_Custom{
		Custom: &agentcoordpb.CustomEvent{Name: coord.CustomTurnIdle},
	}}
}

// oneshotSession wires a canned event stream into runOneshotViaCoord. The
// channel is buffered and pre-filled so the renderer consumes it in order and
// the test never has to synchronise with it.
func oneshotSession(t *testing.T, evs ...*agentcoordpb.AgentEvent) *ownedRunSession {
	t.Helper()
	ch := make(chan *agentcoordpb.AgentEvent, len(evs)+1)
	for _, ev := range evs {
		ch <- ev
	}
	return &ownedRunSession{
		outcome: &coord.RunOutcome{RunID: ownedTestRunID},
		events:  ch,
		cancel:  func() {},
	}
}

// TestRunOneshotViaCoord_EmptyAnswerIsAFailure pins a fix: a container --one-shot
// run that streamed zero answer bytes is this project's signature silent
// no-op: the code already WARNS about it and then returns nil, so `ctxloom run
// --one-shot ... > out.txt` leaves an empty file and exit 0. A caller cannot tell
// that from a legitimately empty answer because there is no such thing —
// a oneshot that produced nothing did not do the one job it was asked to do.
func TestRunOneshotViaCoord_EmptyAnswerIsAFailure(t *testing.T) {
	sess := oneshotSession(t, ownedTurnIdle())
	var out bytes.Buffer

	err := runOneshotViaCoord(t.Context(), sess, "", "claude-code", "do the thing", &out)

	require.Error(t, err, "a oneshot that delivered zero answer bytes must not exit 0")
	var exitErr *ExitError
	require.True(t, errors.As(err, &exitErr), "the failure must carry a nonzero exit code, got %T", err)
	assert.NotZero(t, exitErr.Code)
	assert.Empty(t, out.String())
}

// TestRunOneshotViaCoord_StreamsAndSucceeds is the discriminator: a run that
// DID deliver text still exits 0 and still prints exactly what it streamed.
// Without this, the empty-answer fix above could be satisfied by failing
// everything.
func TestRunOneshotViaCoord_StreamsAndSucceeds(t *testing.T) {
	sess := oneshotSession(t,
		ownedFinalStart("m1"),
		ownedDelta("m1", "hello "),
		ownedDelta("m1", "world"),
		ownedTurnIdle(),
	)
	var out bytes.Buffer

	err := runOneshotViaCoord(t.Context(), sess, "", "claude-code", "do the thing", &out)

	require.NoError(t, err)
	assert.Equal(t, "hello world\n", out.String(), "the streamed answer, newline-terminated")
}

// TestRunOneshotViaCoord_ConcurrentDeltasAreRaceFree pins a fix: the renderer
// runs in its own goroutine and kept appending to the answer buffer while the
// main goroutine read it at the turn boundary — the boundary is signalled from
// INSIDE the loop that keeps writing, so the read is not ordered after the
// writes. Under -race this is a reported data race; without it, it is a
// truncated or garbled answer nobody can reproduce.
//
// The stream keeps producing deltas after the boundary precisely to keep the
// renderer writing while the main goroutine reads.
func TestRunOneshotViaCoord_ConcurrentDeltasAreRaceFree(t *testing.T) {
	evs := []*agentcoordpb.AgentEvent{ownedFinalStart("m1"), ownedDelta("m1", "answer")}
	evs = append(evs, ownedTurnIdle())
	for range 500 {
		evs = append(evs, ownedDelta("m1", "x"))
	}
	sess := oneshotSession(t, evs...)

	done := make(chan error, 1)
	go func() { done <- runOneshotViaCoord(t.Context(), sess, "", "claude-code", "p", &strings.Builder{}) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("runOneshotViaCoord did not return at the turn boundary")
	}
}

// TestRenderOwnedRunEvents_IgnoresOtherRuns pins the filter the renderer
// already applies. The subscription is unfiltered (WatchRuns(nil)), so events
// belonging to another run must never contribute to this run's answer.
func TestRenderOwnedRunEvents_IgnoresOtherRuns(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent, 8)
	other := ownedDelta("m1", "not mine")
	other.RunId = "some-other-run"
	ch <- ownedFinalStart("m1")
	ch <- other
	ch <- ownedDelta("m1", "mine")
	ch <- ownedTurnIdle()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	idle := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		_, err := renderOwnedRunEvents(ctx, &out, formatText, ownedTestRunID, ch, idle, true)
		done <- err
	}()

	select {
	case text := <-idle:
		assert.Equal(t, "mine", text, "another run's deltas must not enter this run's answer")
	case <-time.After(10 * time.Second):
		t.Fatal("no turn boundary observed")
	}
	cancel()
	<-done
	assert.Equal(t, "mine", out.String())
}

// TestRenderOwnedRunEvents_SeqGapIsSurfaced pins the gap-detection
// fix: the watchHub ring (consumer.go) is a lossy, per-subscriber-shared
// buffer — a busy coordinator running other concurrent work can drop this
// run's own events out of it, and nothing upstream re-sends them. Every
// event this run's own EngineHost emits carries a strictly-contiguous Seq
// (home.go's emitEvent), so a hole in that sequence is the one signal the
// renderer has that it silently lost data — and it must say so rather than
// finishing as if the (possibly truncated) answer were complete.
func TestRenderOwnedRunEvents_SeqGapIsSurfaced(t *testing.T) {
	start := ownedFinalStart("m1")
	start.Seq = 1
	d1 := ownedDelta("m1", "he")
	d1.Seq = 2
	// Seq 3 never arrives: dropped by the hub's ring under load.
	d2 := ownedDelta("m1", "llo")
	d2.Seq = 4
	idleEv := ownedTurnIdle()
	idleEv.Seq = 5

	ch := make(chan *agentcoordpb.AgentEvent, 8)
	ch <- start
	ch <- d1
	ch <- d2
	ch <- idleEv

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	idle := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		_, err := renderOwnedRunEvents(ctx, io.Discard, formatText, ownedTestRunID, ch, idle, true)
		done <- err
	}()

	select {
	case <-idle:
	case <-time.After(10 * time.Second):
		t.Fatal("no turn boundary observed")
	}
	cancel()
	<-done

	assert.Contains(t, buf.String(), ownedTestRunID, "the gap warning must name the affected run")
	assert.Contains(t, buf.String(), "lost", "a Seq gap must be surfaced as a warning naming what was lost, not silently swallowed")
}
