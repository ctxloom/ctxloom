package transcript

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// signalRecorder reports each Record and each Close on channels, so a test can
// observe them with a bounded wait instead of a sleep.
type signalRecorder struct {
	recorded chan agent.ChatEvent
	closed   chan struct{}
	once     sync.Once
}

func newSignalRecorder() *signalRecorder {
	return &signalRecorder{recorded: make(chan agent.ChatEvent, 16), closed: make(chan struct{})}
}

func (r *signalRecorder) Record(ev agent.ChatEvent) error {
	r.recorded <- ev
	return nil
}

func (r *signalRecorder) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

const teeWait = 2 * time.Second

// TestTee_RecordsWithoutWaitingForTheConsumer pins the half of tee's contract
// that is TRUE and load-bearing: recording happens BEFORE forwarding, so a
// consumer that has not read yet delays no write. The doc comment used to
// state this as "never blocks", which is false of the helper as a whole — the
// forwarding send is unbuffered and blocks exactly as long as the consumer
// takes — and the two promises it made in one breath ("never blocks" and
// "never drops an event") cannot both hold on an unbuffered channel.
func TestTee_RecordsWithoutWaitingForTheConsumer(t *testing.T) {
	rec := newSignalRecorder()
	src := make(chan agent.ChatEvent, 1)
	src <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "hello"}}

	out := tee(rec, src)

	// Deliberately do NOT read out yet.
	select {
	case got := <-rec.recorded:
		require.NotNil(t, got.Entry)
		assert.Equal(t, "hello", got.Entry.Content)
	case <-time.After(teeWait):
		t.Fatal("Record did not run while the consumer was not reading: recording is supposed to precede forwarding")
	}

	// Drain so the goroutine finishes rather than being left parked.
	close(src)
	<-out
	for range out { //nolint:revive // drain to completion
	}
}

// TestTeeAndClose_AbandonedConsumerHoldsTheRecorderOpen pins the CONSEQUENCE
// the corrected doc now states, so that "an abandoned consumer parks the
// goroutine and leaves the Recorder open" is a maintained fact rather than a
// claim in a comment. If a cancellation seam is ever added, this test is the
// one that must change, deliberately.
func TestTeeAndClose_AbandonedConsumerHoldsTheRecorderOpen(t *testing.T) {
	rec := newSignalRecorder()
	src := make(chan agent.ChatEvent, 1)
	src <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: "x"}}
	close(src) // the PRODUCER is finished; only the consumer is absent

	out := TeeAndClose(rec, src)

	// The event is recorded regardless...
	select {
	case <-rec.recorded:
	case <-time.After(teeWait):
		t.Fatal("the event was never recorded")
	}
	// ...but Close cannot run while the forwarding send is parked.
	select {
	case <-rec.closed:
		t.Fatal("Close ran while the event was still unforwarded — the ordering contract changed")
	case <-time.After(50 * time.Millisecond):
	}

	// Read the event: the chain completes and the Recorder is closed.
	<-out
	select {
	case <-rec.closed:
	case <-time.After(teeWait):
		t.Fatal("Close never ran after the consumer drained the stream")
	}
}
