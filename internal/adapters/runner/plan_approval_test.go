package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// planPostures are the postures the tests' plans may be approved for.
var planPostures = []engine.PostureTransition{
	{Posture: "default", Label: "default"},
	{Posture: "acceptEdits", Label: "accept edits", Default: true},
}

// drivePlanFirst drives a run whose posture plans first, its briefing a
// plain turn; the home stamps "plan/p1" as the plan each turn leaves.
func drivePlanFirst(t *testing.T, plansFirst bool) (*fakeEngineHome, *postureEngine) {
	t.Helper()
	home := &fakeEngineHome{planArtifact: "plan/p1"}
	eng := &postureEngine{home: home, postures: make(chan engine.TurnPosture, 8), prompts: make(chan string, 8), holding: make(chan struct{}, 1), release: make(chan struct{})}
	eh := NewEngineHost(context.Background(), nil, string(mock.Name), "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	turn := Turn{Launch: launch.Launch{Engine: mock.Name, Mode: engine.Structured}, Instance: eng, Prompt: "plain",
		approval: &approvalSpec{codec: codec, timeout: time.Minute, transitions: planPostures, plansFirst: plansFirst}}
	require.NoError(t, eh.Drive(context.Background(), turn))
	return home, eng
}

// nextTurn is the next turn's prompt and posture.
func nextTurn(t *testing.T, eng *postureEngine) (string, engine.TurnPosture) {
	t.Helper()
	p := nextPosture(t, eng)
	select {
	case prompt := <-eng.prompts:
		return prompt, p
	case <-time.After(10 * time.Second):
		t.Fatal("no prompt recorded")
		return "", p
	}
}

// deliver hands the run a message the way the coordinator's delivery does.
func deliver(t *testing.T, home *fakeEngineHome, id string, structured map[string]any) {
	t.Helper()
	pm := &agentcoordpb.PeerMessage{MessageId: id, FromAgentId: "parent-harp", Text: "looks good", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE}
	if structured != nil {
		s, err := structpb.NewStruct(structured)
		require.NoError(t, err)
		pm.Structured = s
	}
	home.mu.Lock()
	sink := home.sink
	home.mu.Unlock()
	require.NotNil(t, sink, "the run opened its turn sink")
	require.True(t, sink(pm), "the delivery became a turn")
}

// reportN waits for the n-th automatic turn report (1-based).
func reportN(t *testing.T, home *fakeEngineHome, n int) turnReport {
	t.Helper()
	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.turnReports) >= n
	}, 10*time.Second, 5*time.Millisecond, "turn report %d", n)
	home.mu.Lock()
	defer home.mu.Unlock()
	return home.turnReports[n-1]
}

// TestEngineHost_APlanFirstTurnEndsHoldingItsPlan: a run that plans first
// ends each turn holding a plan, and its report carries the plan's artifact
// and the postures the parent may approve it for, as data.
func TestEngineHost_APlanFirstTurnEndsHoldingItsPlan(t *testing.T) {
	home, eng := drivePlanFirst(t, true)
	_, posture := nextTurn(t, eng)
	assert.Empty(t, posture.Mode, "the briefing runs at the declared posture")
	r := reportN(t, home, 1)
	require.NotNil(t, r.Plan, "the report carries the plan awaiting approval")
	assert.Equal(t, &coord.PlanApproval{Artifact: "plan/p1", Postures: planPostures}, r.Plan)
}

// TestEngineHost_TheParentsApprovalRunsTheNextTurnAtItsPosture: the parent
// approves by sending; the next turn's prompt is the approval notice,
// verbatim, it runs at the approved posture, and every turn after it does
// too — its reports carry no plan to approve.
func TestEngineHost_TheParentsApprovalRunsTheNextTurnAtItsPosture(t *testing.T) {
	home, eng := drivePlanFirst(t, true)
	nextTurn(t, eng)
	reportN(t, home, 1)

	deliver(t, home, "m-approve", map[string]any{"approve_plan": "default"})
	prompt, posture := nextTurn(t, eng)
	assert.Equal(t, "Your plan was approved. Carry it out now. You are running at the permission mode it was approved for.", prompt)
	assert.Equal(t, "default", posture.Mode)
	r := reportN(t, home, 2)
	assert.Nil(t, r.Plan, "an approved run holds no plan")
	assert.Equal(t, "m-approve", r.InReplyTo, "the report still answers the message that started the turn")

	deliver(t, home, "m-next", nil)
	_, posture = nextTurn(t, eng)
	assert.Equal(t, "default", posture.Mode, "the approved posture holds for the run")
}

// TestEngineHost_AnApprovalNamingNoPostureTakesTheDefault: approve_plan
// with an empty posture approves at the default the engine offered.
func TestEngineHost_AnApprovalNamingNoPostureTakesTheDefault(t *testing.T) {
	home, eng := drivePlanFirst(t, true)
	nextTurn(t, eng)
	reportN(t, home, 1)
	deliver(t, home, "m-approve", map[string]any{"approve_plan": ""})
	_, posture := nextTurn(t, eng)
	assert.Equal(t, "acceptEdits", posture.Mode)
}

// TestEngineHost_WhatIsNotAnApprovalKeepsThePlanning: a message without an
// approval, and one naming a posture the engine did not offer, are
// delivered as they are; the run keeps planning and keeps reporting its plan.
func TestEngineHost_WhatIsNotAnApprovalKeepsThePlanning(t *testing.T) {
	for name, structured := range map[string]map[string]any{
		"plain":     nil,
		"unoffered": {"approve_plan": "bypass"},
		"not text":  {"approve_plan": true},
	} {
		t.Run(name, func(t *testing.T) {
			home, eng := drivePlanFirst(t, true)
			nextTurn(t, eng)
			reportN(t, home, 1)
			deliver(t, home, "m-1", structured)
			prompt, posture := nextTurn(t, eng)
			assert.Contains(t, prompt, "looks good", "the message is delivered as it is")
			assert.Empty(t, posture.Mode, "still at the declared posture")
			require.NotNil(t, reportN(t, home, 2).Plan, "still holding a plan")
		})
	}
}

// TestEngineHost_ARunThatDoesNotPlanFirstHasNoPlanToApprove: approve_plan
// on a run that does not plan first moves nothing and is delivered as it is.
func TestEngineHost_ARunThatDoesNotPlanFirstHasNoPlanToApprove(t *testing.T) {
	home, eng := drivePlanFirst(t, false)
	nextTurn(t, eng)
	assert.Nil(t, reportN(t, home, 1).Plan)
	deliver(t, home, "m-1", map[string]any{"approve_plan": "default"})
	prompt, posture := nextTurn(t, eng)
	assert.Contains(t, prompt, "looks good")
	assert.Empty(t, posture.Mode)
}

// TestApprovalSpecFor_CarriesWhetherTheRunPlansFirst: the route a launch
// serves knows whether its run plans first, from the engine's own model.
func TestApprovalSpecFor_CarriesWhetherTheRunPlansFirst(t *testing.T) {
	for mode, want := range map[string]bool{"plan": true, "default": false} {
		l := launch.Launch{Engine: mock.Name, Mode: engine.Structured}
		l.MCP.URL = "http://127.0.0.1:1/mcp"
		l.Permission = engine.PermissionPolicy{Approver: engine.ApproverHuman, Posture: engine.Posture{Engine: mock.Name, Document: map[string]any{"mode": mode}}}
		spec := approvalSpecFor(mock.New(), l)
		require.NotNilf(t, spec, "mode %s routes approvals", mode)
		assert.Equalf(t, want, spec.plansFirst, "mode %s", mode)
	}
}
