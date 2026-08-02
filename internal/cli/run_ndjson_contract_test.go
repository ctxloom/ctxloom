package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// TestRenderOwnedRunEvents_NDJSONCarriesOnlyEntries characterizes how much of
// the NDJSON contract the container structured arm actually delivers.
//
// The contract a structured frontend consumes has three discriminators --
// entry, complete and session (chatEventType*). The go-plugin arm emits all
// three (chatEventToJSON). This arm emits entry only, so a frontend that
// switches on type gets the prose and nothing else: no per-turn usage or cost
// line, and no harness-native session id -- which is the resume handle, so
// "continue this conversation" cannot be offered for a container session at
// all.
//
// This is a CHARACTERIZATION, not an endorsement: it records the gap so that
// closing it is a deliberate act with a visible diff, rather than something a
// later change makes look accidental. Closing it is not a CLI-side edit --
// it needs escalation elsewhere. The session half is recoverable
// here (the coordinator already emits ctxloom/harness_session carrying the
// session id and its resumable flag); the complete half is not, because
// per-turn usage never reaches this stream -- ctxloom/turn_idle carries only
// stop_reason, and usage is folded into the terminal RunCompleted Result.
func TestRenderOwnedRunEvents_NDJSONCarriesOnlyEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ch := make(chan *agentcoordpb.AgentEvent, 8)
	ch <- ownedFinalStart("m-1")
	ch <- ownedDelta("m-1", "hello")
	ch <- ownedMessageCompleted("m-1")
	ch <- &agentcoordpb.AgentEvent{RunId: ownedTestRunID, Payload: &agentcoordpb.AgentEvent_Custom{
		Custom: &agentcoordpb.CustomEvent{Name: "ctxloom/harness_session"},
	}}
	ch <- ownedTurnIdle()

	idle := make(chan string, 4)
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = renderOwnedRunEvents(ctx, &out, formatJSON, ownedTestRunID, ch, idle, true)
	}()

	select {
	case <-idle:
	case <-ctx.Done():
		require.NotErrorIs(t, ctx.Err(), context.DeadlineExceeded, "renderer never reached the turn boundary")
	}
	cancel()
	<-done

	var kinds []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var ev chatEventJSON
		require.NoError(t, json.Unmarshal([]byte(line), &ev), "every line must be a contract event")
		kinds = append(kinds, ev.Type)
	}

	require.NotEmpty(t, kinds)
	for _, k := range kinds {
		assert.Equal(t, chatEventTypeEntry, k,
			"this arm emits only %q today — if it has started emitting %q or %q, this gap is closing and this characterization must be replaced by a conformance assertion",
			chatEventTypeEntry, chatEventTypeComplete, chatEventTypeSession)
	}
}
