package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestEngineHost_ResumeCapabilityRidesTheSessionID pins the JOINT reporting
// of the resume capability and the session key: a resume capability with no
// session key to resume BY is not a capability, and reporting it alone would
// leave a run record claiming resumability it cannot deliver. With one engine process per turn, a key the driver
// reports IS what the next turn's process resumes with: resumable is true
// exactly when a key is known, and nothing is journaled without one.
func TestEngineHost_ResumeCapabilityRidesTheSessionID(t *testing.T) {
	report := func(t *testing.T, sc *eventScript) map[string]any {
		t.Helper()
		home := &fakeEngineHome{}
		eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
		t.Cleanup(eh.Close)
		eh.BindHome(home)
		resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
		require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

		// The turn's boundary is the "everything relayed has been adapted"
		// barrier.
		require.Eventually(t, func() bool {
			for _, n := range home.customNames() {
				if n == coord.CustomTurnIdle {
					return true
				}
			}
			return false
		}, 5*time.Second, 10*time.Millisecond)

		home.mu.Lock()
		defer home.mu.Unlock()
		for _, c := range home.customs {
			if c.Name == coord.CustomHarnessSession {
				return c.Value
			}
		}
		return nil
	}

	t.Run("a session event's id carries both halves", func(t *testing.T) {
		got := report(t, &eventScript{script: []agent.ChatEvent{{Session: &agent.ChatSessionInfo{SessionID: "native-sess-42"}}}})
		require.NotNil(t, got, "a session id must reach the coordinator's journal path")
		assert.Equal(t, "native-sess-42", got["session_id"])
		assert.Equal(t, true, got["resumable"], "a key the driver reported is what the next turn resumes by")
	})

	t.Run("the turn result's key carries both halves", func(t *testing.T) {
		got := report(t, &eventScript{result: engine.TurnResult{NativeKey: "native-sess-43"}})
		require.NotNil(t, got)
		assert.Equal(t, "native-sess-43", got["session_id"])
		assert.Equal(t, true, got["resumable"])
	})

	t.Run("no session key reports neither half", func(t *testing.T) {
		got := report(t, &eventScript{script: []agent.ChatEvent{{Session: &agent.ChatSessionInfo{SessionID: "", Resumable: true}}}})
		assert.Nil(t, got, "a resume capability with no session key to resume by must not be journaled alone")
	})
}
