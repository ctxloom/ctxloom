package runner

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// openGate reports the engine ready for free text, always. It is the state
// every test written before the gate existed implicitly assumed, which is why
// those tests now name it explicitly rather than relying on a nil gate — a nil
// gate REFUSES, and silently satisfying a "no frame was delivered" assertion
// for the wrong reason is precisely the vacuous green this project audits for.
type openGate struct{}

func (openGate) Observe([]byte)      {}
func (openGate) AcceptingText() bool { return true }

// closedGate reports a modal on screen, always.
type closedGate struct{}

func (closedGate) Observe([]byte)      {}
func (closedGate) AcceptingText() bool { return false }

// TestTerminalInject_WithheldWhileAModalIsOpen is the defect this guard exists
// for. The engine is silent and the human is not typing, so BOTH pre-existing
// guards read "safe to write" — which is exactly the state a prompt waiting
// for a human produces. Only the gate can tell the difference.
//
// Asserted on the EFFECT (no frame handed to inject), not on a returned error:
// run reports nothing to anybody, so an error-shaped assertion would pass
// against an injector that wrote the frame anyway.
func TestTerminalInject_WithheldWhileAModalIsOpen(t *testing.T) {
	var injected atomic.Int64
	ti := &TerminalInjector{
		gate:       closedGate{},
		quiet:      5 * time.Millisecond,
		tick:       time.Millisecond,
		maxWait:    30 * time.Millisecond,
		inputQuiet: 5 * time.Millisecond,
		ackWait:    time.Millisecond,
		ackTick:    time.Millisecond,
		count:      func() int { return 1 },
	}
	ti.inject = func(string, string) { injected.Add(1) }
	// Both existing guards deliberately satisfied: engine long silent, human
	// long stopped. A prompt awaiting a decision is silent on both.
	ti.lastWrite.Store(time.Now().Add(-time.Hour).UnixNano())
	ti.lastInput.Store(time.Now().Add(-time.Hour).UnixNano())

	ti.run()

	assert.Zero(t, injected.Load(),
		"a frame must NOT be delivered while the engine reports a modal: the CR that submits it is consumed as \"confirm the highlighted option\", so the wake answers a decision nobody made")
}

// TestTerminalInject_DeliveredWhenTheEngineAcceptsText is the other half, and
// it is not optional. Without it the suppression above is satisfied by an
// injector that never delivers at all — the silent no-op this codebase ships
// most often.
func TestTerminalInject_DeliveredWhenTheEngineAcceptsText(t *testing.T) {
	var injected atomic.Int64
	var consumed atomic.Bool
	ti := &TerminalInjector{
		gate:       openGate{},
		quiet:      5 * time.Millisecond,
		tick:       time.Millisecond,
		maxWait:    500 * time.Millisecond,
		inputQuiet: 5 * time.Millisecond,
		ackWait:    time.Millisecond,
		ackTick:    time.Millisecond,
		count: func() int {
			if consumed.Load() {
				return 0
			}
			return 1
		},
	}
	ti.inject = func(string, string) {
		injected.Add(1)
		consumed.Store(true)
	}
	ti.lastWrite.Store(time.Now().Add(-time.Hour).UnixNano())
	ti.lastInput.Store(time.Now().Add(-time.Hour).UnixNano())

	ti.run()

	assert.Equal(t, int64(1), injected.Load(),
		"with the engine accepting free text and both quiet guards satisfied, the wake must actually be delivered")
}

// TestTerminalInject_ModalSuppressionSurvivesTheDeadline pins the
// non-waivable property. maxWait deliberately waives OUTPUT-quiet so a noisy
// idling engine cannot block a wake forever. If it could waive the gate too,
// the guard would be worth nothing on any session busy enough to reach the
// bound — which is every real one.
func TestTerminalInject_ModalSuppressionSurvivesTheDeadline(t *testing.T) {
	var injected atomic.Int64
	ti := &TerminalInjector{
		gate: closedGate{},
		// quiet is unreachable and maxWait is tiny, so the loop is forced
		// down the deadline path rather than the satisfied-guards path.
		quiet:      time.Hour,
		tick:       time.Millisecond,
		maxWait:    20 * time.Millisecond,
		inputQuiet: time.Millisecond,
		ackWait:    time.Millisecond,
		ackTick:    time.Millisecond,
		count:      func() int { return 1 },
	}
	ti.inject = func(string, string) { injected.Add(1) }
	ti.lastWrite.Store(time.Now().UnixNano()) // engine noisy: output-quiet never met
	ti.lastInput.Store(time.Now().Add(-time.Hour).UnixNano())

	ti.run()

	assert.Zero(t, injected.Load(),
		"reaching maxWait waives output-quiet but must NEVER waive the input gate: a forged decision is unrecoverable and looks like an answer, which is worse than the delayed wake refusing costs")
}

// TestTerminalInject_AnEngineWithNoGateIsRefusedNotAssumedSafe pins
// fail-closed at the capability boundary. An engine that implements no
// InputGate has given no signal, and no signal must delay a wake rather than
// permit one.
func TestTerminalInject_AnEngineWithNoGateIsRefusedNotAssumedSafe(t *testing.T) {
	var injected atomic.Int64
	ti := &TerminalInjector{
		gate:       nil, // explicit: this is the engine-publishes-nothing case
		quiet:      5 * time.Millisecond,
		tick:       time.Millisecond,
		maxWait:    30 * time.Millisecond,
		inputQuiet: 5 * time.Millisecond,
		ackWait:    time.Millisecond,
		ackTick:    time.Millisecond,
		count:      func() int { return 1 },
	}
	ti.inject = func(string, string) { injected.Add(1) }
	ti.lastWrite.Store(time.Now().Add(-time.Hour).UnixNano())
	ti.lastInput.Store(time.Now().Add(-time.Hour).UnixNano())

	ti.run()

	assert.Zero(t, injected.Load(),
		"an engine publishing no InputGate must be REFUSED, not treated as safe by default: silence is not consent")
}

// TestQuietTap_FeedsTheEngineBytesToTheGate pins the plumbing. The gate is
// useless unless it actually sees the engine's output, and quietTap is the
// only place coord already holds every byte.
func TestQuietTap_FeedsTheEngineBytesToTheGate(t *testing.T) {
	var seen [][]byte
	g := &recordingGate{onObserve: func(p []byte) { seen = append(seen, append([]byte(nil), p...)) }}
	var lastWrite atomic.Int64
	tap := &quietTap{dst: discardWriter{}, lastWrite: &lastWrite, gate: g}

	_, err := tap.Write([]byte("hello"))
	assert.NoError(t, err)

	assert.Equal(t, [][]byte{[]byte("hello")}, seen,
		"quietTap must hand the engine's own output to the gate; without it AcceptingText never changes and every wake is refused forever")
	assert.NotZero(t, lastWrite.Load(), "the quiet stamp must still be recorded")
}

type recordingGate struct{ onObserve func([]byte) }

func (r *recordingGate) Observe(p []byte)    { r.onObserve(p) }
func (r *recordingGate) AcceptingText() bool { return true }

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
