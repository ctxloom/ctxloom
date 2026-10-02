package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// startRunAt is testStartRun with the launch's policy replaced.
func startRunAt(runID string, p engine.PermissionPolicy) *agentcoordpb.StartRun {
	l := ownerLaunch("child-harp-1", "claude-code", "fast", "claude-sonnet-5", "/work", "bypass")
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

// The runner reads no posture: a first turn asks for no mode of its own,
// whatever the launch's policy, and the engine runs it at the posture its
// session carries.
func TestEngineHost_FirstTurnAsksForNoModeOfItsOwn(t *testing.T) {
	for _, mode := range []string{"acceptEdits", "plan", "bypass"} {
		p := engine.PermissionPolicy{Posture: engine.Posture{Engine: "claude-code", Document: map[string]any{"mode": mode}}, Approver: engine.ApproverNone, Sandbox: engine.SandboxFull}
		assert.Equalf(t, engine.TurnPosture{}, firstTurnPosture(t, p), "%s", mode)
	}
}
