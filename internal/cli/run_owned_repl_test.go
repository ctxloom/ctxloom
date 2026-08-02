package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// The whole Phase 2a-B host path (this file's package) once had zero tests;
// runOneshotViaCoord and renderOwnedRunEvents since
// gained coverage (run_owned_oneshot_test.go, run_owned_terminal_status_test.go),
// but runStructuredREPLViaCoord — the Transport 2 REPL driver, distinct from
// both — remained untested. This pins its base case: the lead turn's
// boundary arrives, stdin hits EOF with no lines sent, and the loop returns
// cleanly once both the "no more input" and "no pending turns" conditions
// hold — without ever touching sess.coord (SendOwnedRunTurn is only called
// when a stdin line arrives, which this case never does).
func TestRunStructuredREPLViaCoord_EOFAfterLeadTurnReturnsCleanly(t *testing.T) {
	ch := make(chan *agentcoordpb.AgentEvent, 1)
	ch <- ownedTurnIdle()
	sess := &ownedRunSession{
		outcome:        &coord.RunOutcome{RunID: ownedTestRunID},
		events:         ch,
		cancel:         func() {},
		leadTurnIssued: true,
	}

	done := make(chan error, 1)
	go func() {
		var out strings.Builder
		done <- runStructuredREPLViaCoord(t.Context(), sess, formatText, strings.NewReader(""), &out)
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "EOF with no pending turns must return cleanly, not hang or error")
	case <-time.After(5 * time.Second):
		t.Fatal("runStructuredREPLViaCoord did not return once stdin closed and the lead turn's boundary arrived")
	}
}
