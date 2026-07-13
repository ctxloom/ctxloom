package coord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// attachedHome stands up a Home whose run channel has actually attached, so a
// request failure can only be a budget expiry — never "we never got through".
func attachedHome(t *testing.T, c *Coordinator, runID string, env map[string]string) *Home {
	t.Helper()
	h, err := NewHome(context.Background(), HomeConfig{
		URL:     env[EnvCoordURL],
		Token:   env[EnvCoordCred],
		RunID:   runID,
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	require.Eventually(t, h.Attached, 5*time.Second, 10*time.Millisecond,
		"the run channel must attach before the request's failure can be classified")
	return h
}

// TestRequest_DeliveredButSlowIsADeadlineNotUnreachable pins the distinction a
// single ErrCoordinatorUnreachable collapsed: a request the coordinator
// ACCEPTED and is still working on, whose caller budget then expires, is a
// blown budget — not a down coordinator. Reporting it as "unreachable (the
// runner keeps reconnecting)" sent recover_session's caller chasing a phantom
// outage while the distillation ran happily to completion behind it.
func TestRequest_DeliveredButSlowIsADeadlineNotUnreachable(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	c.SetCustomHandlers(map[string]CustomHandler{
		CustomToolPrefix + "slow_tool": func(ctx context.Context, caller Identity, args json.RawMessage) (json.RawMessage, error) {
			<-release // the host is WORKING, not gone
			return json.RawMessage(`{}`), nil
		},
	})

	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)
	h := attachedHome(t, c, out.RunID, env)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, rerr := h.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_Custom{Custom: &agentcoordpb.CustomRequest{
			Name:  CustomToolPrefix + "slow_tool",
			Value: &structpb.Struct{},
		}},
	})

	require.Error(t, rerr)
	assert.ErrorIs(t, rerr, context.DeadlineExceeded,
		"a delivered-but-slow request is a blown budget, and must surface as one")
	assert.NotErrorIs(t, rerr, ErrCoordinatorUnreachable,
		"the coordinator was reachable and running the request — calling it unreachable is a misdiagnosis")
}

// TestRequest_NeverAttachedIsUnreachable keeps the other half honest: when the
// run channel never attached, the caller genuinely could not get through, and
// ErrCoordinatorUnreachable remains the right answer.
func TestRequest_NeverAttachedIsUnreachable(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)

	h, err := NewHome(context.Background(), HomeConfig{
		URL:     env[EnvCoordURL],
		Token:   env[EnvCoordCred],
		RunID:   "run-not-mine", // Hello is rejected; the channel never attaches
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, rerr := h.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}},
	})

	assert.False(t, h.Attached(), "the rejected Hello must not count as an attach")
	require.ErrorIs(t, rerr, ErrCoordinatorUnreachable)
}
