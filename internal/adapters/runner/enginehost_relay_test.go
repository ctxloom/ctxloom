package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The StartRun answer carries what the launch reported (task #18): the
// runner's stderr surfaces only when a run fails, so on a run that starts,
// a skipped command or skill reaches the originator on this answer or not at
// all. A reissued StartRun (a reconnect) answers the same.
//
// MUTATION: drop the attach in startRun → red.
func TestEngineHost_StartRunRelaysWhatTheLaunchReported(t *testing.T) {
	reported := report.Findings{
		{Text: "skill deploy skipped: it carries no SKILL.md", Remedy: "add the SKILL.md"},
		{Kind: report.KindApply, Text: "runner: sweep: list the ownership record's writers: boom"},
	}
	sc := &eventScript{script: []agent.ChatEvent{{Complete: &agent.TurnMeta{StopReason: "end_turn"}}}}
	eh := NewEngineHost(context.Background(), nil, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindRunner(testRunner{eh: eh, inst: sc, findings: reported})
	eh.BindHome(&fakeEngineHome{})

	req := &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}}
	for _, attempt := range []string{"first", "reissued"} {
		resp := handleBounded(t, eh, req)
		require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		got := resp.GetStartRun().GetFindings()
		require.Len(t, got, 2, attempt)
		assert.Equal(t, reported[0].Text, got[0].GetText(), attempt)
		assert.Equal(t, reported[0].Remedy, got[0].GetRemedy(), attempt)
		assert.Equal(t, reported[1].Text, got[1].GetText(), attempt)
	}
}
