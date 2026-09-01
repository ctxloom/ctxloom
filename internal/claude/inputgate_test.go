package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The backend itself must satisfy the capability, because that is how the host
// finds it — a type assertion on the backend it already holds. If this stops
// compiling the gate is invisible at runtime and every wake is silently
// refused, with nothing to say why.
var _ agent.InputGate = (*ClaudeCode)(nil)

func TestInputGate_StartsClosedBecauseNothingHasBeenObserved(t *testing.T) {
	var g inputGate
	assert.False(t, g.AcceptingText(),
		"the zero value must fail CLOSED: until claude's caret has actually been seen there is no evidence a write would land in a text field, and assuming it would is what forges a decision")
}

func TestInputGate_CaretHiddenMeansAModalIsUp(t *testing.T) {
	var g inputGate
	g.Observe([]byte("\x1b[?25h")) // establish the open state first
	require.True(t, g.AcceptingText(), "precondition: a shown caret opens the gate")

	g.Observe([]byte("some output\x1b[?25lmore output"))

	assert.False(t, g.AcceptingText(),
		"a hidden caret is claude's modal marker; the gate must close so the wake's carriage return is not consumed as \"confirm the highlighted option\"")
}

func TestInputGate_CaretShownReopensTheGate(t *testing.T) {
	var g inputGate
	g.Observe([]byte("\x1b[?25l"))
	require.False(t, g.AcceptingText(), "precondition: a hidden caret closes the gate")

	g.Observe([]byte("\x1b[?25h"))

	assert.True(t, g.AcceptingText(),
		"once the modal closes and the caret returns, ordinary wakes must resume; a gate that never reopens is a silently disabled feature")
}

// TestInputGate_MarkerSplitAcrossWritesIsStillSeen is the one the plan singles
// out. The pty chunks by buffer size, not by escape sequence, so a marker can
// straddle two writes. Missing a HIDE misses in the OPEN direction — the
// injector would believe no modal was up and write into one.
func TestInputGate_MarkerSplitAcrossWritesIsStillSeen(t *testing.T) {
	for _, split := range []int{1, 2, 3, 4, 5} {
		hide := "\x1b[?25l"
		var g inputGate
		g.Observe([]byte("\x1b[?25h"))
		require.True(t, g.AcceptingText(), "precondition: gate open before the split marker")

		g.Observe([]byte(hide[:split]))
		g.Observe([]byte(hide[split:]))

		assert.False(t, g.AcceptingText(),
			"a hide marker split after %d byte(s) must still close the gate: the tail of the previous write has to be joined before scanning, or the sequence is missed in the permissive direction", split)
	}
}

// TestInputGate_LastTransitionInOneWriteWins pins that a single write carrying
// several transitions is read as its FINAL state. A redraw legitimately emits
// both, and taking the first would leave the gate describing a state the
// engine has already left.
func TestInputGate_LastTransitionInOneWriteWins(t *testing.T) {
	var g inputGate
	g.Observe([]byte("\x1b[?25l" + "painting" + "\x1b[?25h"))
	assert.True(t, g.AcceptingText(), "hide-then-show in one write ends SHOWN")

	var g2 inputGate
	g2.Observe([]byte("\x1b[?25h" + "painting" + "\x1b[?25l"))
	assert.False(t, g2.AcceptingText(), "show-then-hide in one write ends HIDDEN")
}

// TestInputGate_UnrelatedOutputDoesNotChangeState guards against a scanner so
// loose it trips on ordinary text — which would make the gate flap and either
// block every wake or permit one mid-modal.
func TestInputGate_UnrelatedOutputDoesNotChangeState(t *testing.T) {
	var g inputGate
	g.Observe([]byte("\x1b[?25h"))
	require.True(t, g.AcceptingText())

	// Neighbouring private modes and plain text must all be inert, including
	// bracketed paste (ESC[?2004), which the measurement found useless here
	// precisely because it is set in every state.
	g.Observe([]byte("\x1b[?2004h\x1b[2J\x1b[?25 not a marker \x1b[?255l"))

	assert.True(t, g.AcceptingText(),
		"only the exact DECTCEM sequences may move the gate; ESC[?2004 in particular is set in EVERY measured state including both modals, so keying on it would never fire")
}

func TestInputGate_EmptyWriteIsInert(t *testing.T) {
	var g inputGate
	g.Observe([]byte("\x1b[?25h"))
	g.Observe(nil)
	g.Observe([]byte{})
	assert.True(t, g.AcceptingText(), "an empty write carries no evidence and must not change the state")
}
