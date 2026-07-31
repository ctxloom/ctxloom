package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nilSessionHistory answers "there is no such session" the way the
// SessionHistory contract allows: no session, no error.
type nilSessionHistory struct{ agent.SessionHistory }

func (nilSessionHistory) GetSession(_, id string) (*agent.Session, error) {
	if id == "present-but-empty" {
		return &agent.Session{ID: id}, nil
	}
	return nil, nil
}

type nilHistoryBackend struct{ agent.Backend }

func (nilHistoryBackend) Name() string                  { return "nilhist" }
func (nilHistoryBackend) History() agent.SessionHistory { return nilSessionHistory{} }

// A backend that reports "no such session" as (nil, nil) must not have that
// answer turn into an EMPTY SESSION by the time it reaches the host. An empty
// non-nil session is a real answer — a session that exists and has no turns yet
// — so a caller that cannot tell the two apart renders a live-but-silent
// session and a missing one identically.
func TestGetSession_AbsentSessionSurvivesTheWire(t *testing.T) {
	client, _ := plugin.TestPluginGRPCConn(t, false, map[string]plugin.Plugin{
		LLMPluginKey: &LLMGRPCPlugin{Impl: nilHistoryBackend{}},
	})
	t.Cleanup(func() { _ = client.Close() })

	raw, err := client.Dispense(LLMPluginKey)
	require.NoError(t, err)
	c := raw.(*GRPCClient)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := c.GetSession(ctx, "no-such-session")
	require.NotErrorIs(t, err, context.DeadlineExceeded, "the RPC parked instead of answering")
	require.Error(t, err, "an absent session must not arrive as an empty one")
	assert.Contains(t, err.Error(), "no-such-session")
	assert.Nil(t, sess)

	// The distinguishing half: a session that genuinely exists and has no
	// turns yet must still come back as a real, empty session.
	empty, err := c.GetSession(ctx, "present-but-empty")
	require.NoError(t, err)
	require.NotNil(t, empty, "an existing empty session is a real answer, not an absence")
	assert.Empty(t, empty.Entries)
}
