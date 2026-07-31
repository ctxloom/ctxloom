package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// TestRunStructuredREPLViaCoord_NoLeadTurnDoesNotHangAtEOF pins the REPL's
// exit condition against the case where the run opens with NOTHING in flight.
//
// The loop returns when stdin is closed AND every issued turn has reached its
// boundary, so its opening count must match the number of turns actually
// issued. A structured run is explicitly allowed to open with no lead:
// Coordinator.StartOwnedRun refuses an empty prompt only for a ONE-SHOT run,
// and issueStartRun attaches Input only when the lead is non-empty. So a
// structured run whose assembled context and prompt are both empty issues no
// opening turn at all, and no turn boundary will ever arrive for it.
//
// Counting an opening turn that was never issued leaves the loop waiting at
// EOF for a boundary that cannot come -- an indefinite hang on a run that has
// nothing to wait for, releasable only by the run context.
func TestRunStructuredREPLViaCoord_NoLeadTurnDoesNotHangAtEOF(t *testing.T) {
	// No events at all: no lead turn was issued, so nothing signals a
	// boundary, and stdin is empty so no further turn is ever sent.
	sess := &ownedRunSession{
		outcome:        &coord.RunOutcome{RunID: ownedTestRunID},
		events:         make(chan *agentcoordpb.AgentEvent),
		cancel:         func() {},
		leadTurnIssued: false,
	}

	done := make(chan error, 1)
	go func() {
		var out strings.Builder
		done <- runStructuredREPLViaCoord(t.Context(), sess, formatText, strings.NewReader(""), &out)
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "a run with no lead turn and no input must return cleanly")
	case <-time.After(5 * time.Second):
		t.Fatal("runStructuredREPLViaCoord waited at EOF for a lead-turn boundary that was never going to arrive")
	}
}

// TestRunStructuredREPLViaCoord_LeadTurnIsStillAwaited is the other half: when
// a lead turn WAS issued, the loop must still wait for its boundary rather
// than returning the moment stdin closes -- otherwise the fix above would
// truncate the opening turn of every ordinary structured run.
func TestRunStructuredREPLViaCoord_LeadTurnIsStillAwaited(t *testing.T) {
	events := make(chan *agentcoordpb.AgentEvent, 1)
	sess := &ownedRunSession{
		outcome:        &coord.RunOutcome{RunID: ownedTestRunID},
		events:         events,
		cancel:         func() {},
		leadTurnIssued: true,
	}

	done := make(chan error, 1)
	go func() {
		var out strings.Builder
		done <- runStructuredREPLViaCoord(t.Context(), sess, formatText, strings.NewReader(""), &out)
	}()

	select {
	case <-done:
		t.Fatal("returned at EOF while the lead turn was still in flight")
	case <-time.After(250 * time.Millisecond):
	}

	events <- ownedTurnIdle()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("did not return once the lead turn reached its boundary")
	}
}
