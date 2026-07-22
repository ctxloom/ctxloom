package transcript

import (
	"context"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CoordinatedRecorder fixes outer-petal: GRPCClient.Chat (and
// coord/enginehost.go's analogous seam) used to let two INDEPENDENT
// goroutines — the inbound user-turn tap and the outbound backend-event tee —
// call a shared Recorder's Record method directly. fileRecorder's own mutex
// keeps that data-race-free, but it does nothing to make the resulting
// record ORDER (and therefore Seq assignment) reflect a coordinated policy:
// which goroutine's write landed first was decided by Go's mutex-acquisition
// scheduling, not anything the caller controlled or could reason about.
//
// A CoordinatedRecorder removes the "two independent, uncoordinated
// goroutines sharing one seq counter" shape entirely: exactly one internal
// goroutine ever calls the wrapped Recorder's Record — every producer
// (inbound tap, outbound tee, or any other source) hands its ChatEvent to
// that owner via Submit instead of touching the Recorder itself. This does
// not (and cannot, without risking deadlock against a hypothetical backend
// that withholds all output until it receives input) impose a strict
// cross-source precedence like "session always first" — genuinely
// concurrent, causally-unrelated producers still interleave according to
// real submission timing. What it DOES guarantee: the Recorder is never
// entered by more than one caller, so record order is a well-defined
// function of Submit call arrival at the single owner, not an artifact of
// unfair lock contention layered on top of that arrival order.
type CoordinatedRecorder struct {
	rec  Recorder
	ch   chan agent.ChatEvent
	wg   sync.WaitGroup
	done chan struct{}
}

// NewCoordinatedRecorder starts the owning goroutine and returns immediately.
// producers is the exact number of goroutines that will call Submit — it
// must be known and fixed up front (registered synchronously here, via
// wg.Add, before the internal "wait for everyone" goroutine starts) because
// sync.WaitGroup requires every Add with a positive delta to happen before
// any Wait call that could observe the counter at zero; discovering
// producers lazily via a separate NewProducer method would race a Wait
// goroutine started before the first producer registered. Each producer
// calls ProducerDone exactly once when it will submit no more events. Once
// every producer has called ProducerDone, the owning goroutine drains any
// remaining buffered events, closes rec, and closes the channel Done
// returns.
func NewCoordinatedRecorder(rec Recorder, producers int) *CoordinatedRecorder {
	cr := &CoordinatedRecorder{
		rec:  rec,
		ch:   make(chan agent.ChatEvent),
		done: make(chan struct{}),
	}
	cr.wg.Add(producers)
	go func() {
		cr.wg.Wait()
		close(cr.ch)
	}()
	go func() {
		defer close(cr.done)
		defer func() { _ = cr.rec.Close() }()
		for ev := range cr.ch {
			_ = cr.rec.Record(ev) // best-effort; matches Tee's own contract
		}
	}()
	return cr
}

// ProducerDone signals that one of the producers counted at construction
// will submit no more events. Once every producer has called ProducerDone,
// the wrapped Recorder is closed.
func (cr *CoordinatedRecorder) ProducerDone() {
	cr.wg.Done()
}

// Submit hands ev to the single owning goroutine for recording, blocking
// until accepted or ctx is done — mirroring the "never blocks the live chat
// on a full stall" discipline the rest of this package follows.
func (cr *CoordinatedRecorder) Submit(ctx context.Context, ev agent.ChatEvent) {
	select {
	case cr.ch <- ev:
	case <-ctx.Done():
	}
}

// Done reports when the wrapped Recorder has been closed: every registered
// producer finished AND every event it submitted was drained.
func (cr *CoordinatedRecorder) Done() <-chan struct{} {
	return cr.done
}
