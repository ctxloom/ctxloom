package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// sessionOnlyChat emits exactly one Session event with the scripted id and
// resume capability, then ends its stream. It exists to drive adapt's Session
// arm in isolation, which the shared scriptedChat cannot do (it hard-codes a
// non-empty session id).
type sessionOnlyChat struct {
	sessionID string
	resumable bool
}

func (s *sessionOnlyChat) Chat(ctx context.Context, _ agent.ChatRequest, _ <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	select {
	case out <- agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: s.sessionID, Resumable: s.resumable}}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// TestEngineHost_ResumeCapabilityRidesTheSessionID pins the JOINT reporting
// of the one-shot resume gate's two halves. A review row read the
// `SessionID != ""` guard as silently dropping a `Resumable: true` engine's
// capability; it is deliberate. The gate (oneShotReady, children.go) tears an
// engine down only when `Resumable && HarnessSessionID != ""`, so a resume
// capability with no session key to resume BY is not a capability — and
// journaling it alone would leave a run record claiming resumability it cannot
// deliver, which is the failure the gate exists to prevent.
//
// The coordinator's other Session-event consumer (children.go's legacy
// non-viaStartRun arm) guards on the same condition, so the two adaptation
// paths cannot disagree about the same engine.
func TestEngineHost_ResumeCapabilityRidesTheSessionID(t *testing.T) {
	report := func(t *testing.T, sc *sessionOnlyChat) map[string]any {
		t.Helper()
		home := &fakeEngineHome{}
		eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
		t.Cleanup(eh.Close)
		eh.BindHome(home)
		resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
		require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

		// The stream ends after the one Session event, so RunExited is the
		// deterministic "adapt has seen everything" barrier.
		require.Eventually(t, func() bool {
			home.mu.Lock()
			defer home.mu.Unlock()
			return len(home.exited) == 1
		}, 5*time.Second, 10*time.Millisecond)

		home.mu.Lock()
		defer home.mu.Unlock()
		for _, c := range home.customs {
			if c.Name == CustomHarnessSession {
				return c.Value
			}
		}
		return nil
	}

	t.Run("session id carries both halves", func(t *testing.T) {
		got := report(t, &sessionOnlyChat{sessionID: "native-sess-42", resumable: true})
		require.NotNil(t, got, "a session id must reach the coordinator's journal path")
		assert.Equal(t, "native-sess-42", got["session_id"])
		assert.Equal(t, true, got["resumable"], "the LIVE loadSession capability rides the same event")
	})

	t.Run("not resumable is reported as such", func(t *testing.T) {
		got := report(t, &sessionOnlyChat{sessionID: "native-sess-42", resumable: false})
		require.NotNil(t, got)
		assert.Equal(t, false, got["resumable"])
	})

	t.Run("no session id reports neither half", func(t *testing.T) {
		got := report(t, &sessionOnlyChat{sessionID: "", resumable: true})
		assert.Nil(t, got, "a resume capability with no session key to resume by must not be journaled alone")
	})
}
