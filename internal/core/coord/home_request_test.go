package coord

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// expiredOnCue is a caller budget that expires exactly when the test says so:
// Done closes on expire(), after which Err is DeadlineExceeded. It carries a
// deadline so Request does not wrap it in its own default budget. A wall-clock
// timeout cannot order "the coordinator accepted the request" before "the
// budget ran out" — under load the timer can fire first.
type expiredOnCue struct {
	context.Context
	done chan struct{}
}

func newExpiredOnCue() *expiredOnCue {
	return &expiredOnCue{Context: context.Background(), done: make(chan struct{})}
}

func (c *expiredOnCue) Deadline() (time.Time, bool) { return time.Now().Add(time.Hour), true }
func (c *expiredOnCue) Done() <-chan struct{}       { return c.done }
func (c *expiredOnCue) expire()                     { close(c.done) }
func (c *expiredOnCue) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// TestRequest_DeliveredButSlowIsADeadlineNotUnreachable pins the distinction a
// single runnerHooks.ErrCoordinatorUnreachable collapsed: a request the coordinator
// ACCEPTED and is still working on, whose caller budget then expires, is a
// blown budget — not a down coordinator. Reporting it as "unreachable (the
// runner keeps reconnecting)" sent recover_session's caller chasing a phantom
// outage while the distillation ran happily to completion behind it.
//
// The request rides the child's OWN runner (childHome): a run has exactly one
// runner, and a second Home dialed on the child's credential contends with it
// for the run channel. The budget expires only once the host app holds the
// request, so "delivered, then expired" is forced rather than hoped for.
func TestRequest_DeliveredButSlowIsADeadlineNotUnreachable(t *testing.T) {
	resetStrictness(t)
	delivered := make(chan struct{})
	var deliveredOnce sync.Once
	release := make(chan struct{})
	// Released before the coordinator's Close (a t.Cleanup, which runs after
	// this defer), so Close never joins a handler still parked on it.
	defer close(release)
	c := newTestCoordinatorWithHost(t, researcherSpawner(), &recordingHostApp{fn: func(context.Context, Identity, HostRequest) (HostResult, error) {
		deliveredOnce.Do(func() { close(delivered) })
		<-release // the host is WORKING, not gone
		return HostResult{Body: json.RawMessage(`{}`)}, nil
	}})

	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)
	require.Eventually(t, h.Attached, conformanceWait, 10*time.Millisecond,
		"the child's runner must attach its run channel before the request's failure can be classified")

	ctx := newExpiredOnCue()
	go func() {
		<-delivered
		ctx.expire()
	}()
	_, rerr := h.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_Host{Host: &agentcoordpb.HostRequest{Tool: "slow_tool", Args: &structpb.Struct{}}},
	})

	require.Error(t, rerr)
	assert.ErrorIs(t, rerr, context.DeadlineExceeded,
		"a delivered-but-slow request is a blown budget, and must surface as one")
	assert.NotErrorIs(t, rerr, runnerHooks.ErrCoordinatorUnreachable,
		"the coordinator was reachable and running the request — calling it unreachable is a misdiagnosis")
}

// TestRequest_NeverAttachedIsUnreachable keeps the other half honest: when the
// run channel never attached, the caller genuinely could not get through, and
// runnerHooks.ErrCoordinatorUnreachable remains the right answer.
func TestRequest_NeverAttachedIsUnreachable(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)

	h, err := runnerHooks.NewHome(context.Background(), TestHomeConfig{
		Reporter: termSink(),
		URL:      env[EnvCoordURL],
		Token:    env[EnvCoordCred],
		RunID:    "run-not-mine", // Hello is rejected; the channel never attaches
		Harness:  "mock",
		Version:  "test",
		Harp:     env["CTXLOOM_SESSION_HARP"],
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, rerr := h.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}},
	})

	assert.False(t, h.Attached(), "the rejected Hello must not count as an attach")
	require.ErrorIs(t, rerr, runnerHooks.ErrCoordinatorUnreachable)
}
