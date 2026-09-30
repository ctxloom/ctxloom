package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// startRunAt is testStartRun with the launch's policy replaced.
func startRunAt(runID string, p engine.PermissionPolicy) *agentcoordpb.StartRun {
	l := ownerLaunch("child-harp-1", "claude-code", "fast", "claude-sonnet-5", "/work", agent.PermissionBypass)
	l.Identity.RunID = runID
	l.Identity.Depth = 1
	l.Prompt = "do the thing"
	l.Permission = p
	return &agentcoordpb.StartRun{RunId: runID, Launch: coordgrpc.EncodeLaunch(l)}
}

// firstTurnPosture drives a StartRun at policy p and returns the posture
// the driver was handed for the first turn.
func firstTurnPosture(t *testing.T, p engine.PermissionPolicy) engine.TurnPosture {
	t.Helper()
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: startRunAt("run-1", p)}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.Eventually(t, func() bool { return home.customValue(coord.CustomTurnIdle) != nil }, 5*time.Second, 10*time.Millisecond)
	sc.Mu.Lock()
	defer sc.Mu.Unlock()
	require.NotEmpty(t, sc.Turns)
	return sc.Turns[0].Posture
}

// Every turn carries the run's posture: the resolved mode — which is how
// it reaches a process resumed by key, where a mode does not survive.
func TestEngineHost_TurnCarriesTheLaunchPosture(t *testing.T) {
	got := firstTurnPosture(t, engine.PermissionPolicy{Mode: engine.PermissionAcceptEdits, Ceiling: engine.PermissionAcceptEdits, Approver: engine.ApproverNone})
	assert.Equal(t, engine.TurnPosture{Mode: engine.PermissionAcceptEdits}, got)

	planFirst := engine.PermissionPolicy{Mode: engine.PermissionPlan, AfterPlan: engine.Provide(engine.PermissionAcceptEdits), Ceiling: engine.PermissionAcceptEdits}
	assert.Equal(t, engine.PermissionPlan, firstTurnPosture(t, planFirst).Mode, "a plan-first run starts its first turn in plan")
}

// Bypass never rides a turn: it stays on the launch argv.
func TestEngineHost_BypassTurnAsksForNoMode(t *testing.T) {
	got := firstTurnPosture(t, engine.PermissionPolicy{Mode: engine.PermissionBypass, Ceiling: engine.PermissionBypass})
	assert.Equal(t, engine.TurnPosture{}, got)
}
