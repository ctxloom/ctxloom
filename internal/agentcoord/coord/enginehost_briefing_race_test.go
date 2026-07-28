package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// U021-F01: SetTurnSink's closure and the briefing goroutine both send to
// the same UNBUFFERED `in`, from two different goroutines, with no ordering
// between them. If coordinator mail is already queued at standup
// (issueStartRun's opportunistic pushMail, called immediately after
// startRun returns), its delivery can win that race and land as the
// child's FIRST turn, with the briefing (composed context + prompt)
// arriving SECOND — every signal still reports success.
//
// This test forces the race directly: it registers the turn sink via
// startRun (which also launches the briefing goroutine), then calls the
// sink from a fresh goroutine as fast as possible — no synchronization
// that would let the briefing "naturally" win first, as
// TestEngineHost_TurnSinkDeliversFramedMail's require.Eventually already
// does. scriptedChat's turnGate blocks its Chat loop after recording each
// turn's text, so whichever send wins is captured deterministically as
// texts[0] before the second one is ever read.
func TestEngineHost_TurnSink_NeverRacesAheadOfBriefing(t *testing.T) {
	const iterations = 30
	for i := 0; i < iterations; i++ {
		gate := make(chan struct{})
		home := &fakeEngineHome{}
		sc := &scriptedChat{turnGate: gate}
		eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
		eh.BindHome(home)

		resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
		require.Equal(t, int32(0), resp.GetStatus().GetCode())

		home.mu.Lock()
		sink := home.sink
		home.mu.Unlock()
		require.NotNil(t, sink, "startRun must register the turn sink")

		// Race it: call sink AS SOON AS POSSIBLE, with nothing waiting for
		// the briefing to land first.
		go func() { sink(&agentcoordpb.PeerMessage{MessageId: "m-race", FromAgentId: "parent-harp", Text: "queued mail"}) }()

		if !assert.Eventually(t, func() bool { return len(sc.recordedTexts()) >= 1 }, 5*time.Second, time.Millisecond,
			"iteration %d: neither the briefing nor the mail was ever delivered", i) {
			close(gate)
			eh.Close()
			return
		}
		first := sc.recordedTexts()[0]
		close(gate) // let the second turn through

		if !assert.Eventually(t, func() bool { return len(sc.recordedTexts()) >= 2 }, 5*time.Second, time.Millisecond,
			"iteration %d: the second turn never landed", i) {
			eh.Close()
			return
		}

		if !assert.Contains(t, first, "do the thing",
			"iteration %d: the briefing must always be the FIRST turn, never raced ahead of by queued coordinator mail", i) {
			eh.Close()
			return
		}
		eh.Close()
	}
}
