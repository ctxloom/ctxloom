package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

func ownedMessageCompleted(msgID string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{RunId: ownedTestRunID, Payload: &agentcoordpb.AgentEvent_MessageCompleted{
		MessageCompleted: &agentcoordpb.MessageCompleted{MessageId: msgID},
	}}
}

// TestRenderOwnedRunEvents_CompletedMessageIsForgotten pins the item
// lifecycle's closing half. The renderer learns which message ids belong to the
// FINAL channel from MessageStarted and remembers them so their deltas can be
// told apart from the REASONING/LOG scratchpad. The contract those ids follow
// is started -> delta* -> completed, so a completed id is spent: nothing may
// ever arrive under it again, and holding it costs a map entry per assistant
// message for the whole life of the run this renderer is driving — bounded
// for its current caller (a --print container oneshot), but the renderer
// itself makes no such assumption, which is what this test pins.
//
// The observable half of "forgotten" is that a delta arriving under a spent id
// is no longer treated as FINAL text. That is the same discipline the sibling
// adapter over this exact stream already applies (internal/operations/
// sessionfeed.go drops its per-message state on MessageCompleted, so a late
// delta finds nothing to append to).
func TestRenderOwnedRunEvents_CompletedMessageIsForgotten(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ch := make(chan *agentcoordpb.AgentEvent, 8)
	ch <- ownedFinalStart("m-1")
	ch <- ownedDelta("m-1", "answer")
	ch <- ownedMessageCompleted("m-1")
	// Protocol violation: a delta under an id the stream already closed. It
	// must not be appended to the answer, because the renderer no longer holds
	// any claim that "m-1" was ever a FINAL message.
	ch <- ownedDelta("m-1", "-LATE")
	ch <- ownedTurnIdle()

	idle := make(chan string, 4)
	var out strings.Builder
	done := make(chan string, 1)
	go func() {
		text, _ := renderOwnedRunEvents(ctx, &out, formatText, ownedTestRunID, ch, idle, true)
		done <- text
	}()

	select {
	case text := <-idle:
		assert.Equal(t, "answer", text,
			"a delta under a completed message id must not reach the answer")
		assert.NotContains(t, out.String(), "LATE",
			"a delta under a completed message id must not be rendered either")
	case <-ctx.Done():
		require.NotErrorIs(t, ctx.Err(), context.DeadlineExceeded,
			"the renderer never reached the turn boundary")
	}
	cancel()
	<-done
}
