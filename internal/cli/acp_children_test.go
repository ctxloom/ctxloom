package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// drainChildUpdates runs adaptChildWatch over events, closes the stream, and
// collects everything it emitted. Every wait is bounded: a parked adapter must
// fail the test, never hang the package's 10-minute timeout.
func drainChildUpdates(t *testing.T, snapshot *agentcoordpb.ListRunsResult, evs []*agentcoordpb.AgentEvent) []acpagent.ChildUpdate {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := make(chan *agentcoordpb.AgentEvent, len(evs))
	for _, e := range evs {
		events <- e
	}
	close(events)

	out := make(chan acpagent.ChildUpdate, len(evs)+1)
	done := make(chan struct{})
	go func() {
		adaptChildWatch(ctx, snapshot, events, out)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("adaptChildWatch did not return after its event stream closed")
	}

	var got []acpagent.ChildUpdate
	for u := range out {
		got = append(got, u)
	}
	return got
}

func runStarted(runID, harp, role string) *agentcoordpb.AgentEvent {
	return &agentcoordpb.AgentEvent{
		RunId: runID,
		Payload: &agentcoordpb.AgentEvent_RunStarted{RunStarted: &agentcoordpb.RunStarted{
			Agent: &agentcoordpb.AgentIdentity{AgentId: harp, Role: role},
		}},
	}
}

// adaptChildWatch translates exactly three of the sixteen AgentEvent payload
// variants into ChildUpdates. This characterizes all of it — the three
// translations, the harp bookkeeping across them, and the deliberate silence on
// everything else — so the shape can be refactored without changing what an ACP
// client sees.
func TestAdaptChildWatch_TranslatesTheThreeSurfacedVariants(t *testing.T) {
	t.Run("run started carries the role as its text", func(t *testing.T) {
		got := drainChildUpdates(t, &agentcoordpb.ListRunsResult{},
			[]*agentcoordpb.AgentEvent{runStarted("r1", "brave-otter", "reviewer")})
		require.Len(t, got, 1)
		assert.Equal(t, acpagent.ChildUpdate{Harp: "brave-otter", Kind: acpagent.ChildUpdateStarted, Text: "reviewer"}, got[0])
	})

	t.Run("a role-less start falls back to a generic label", func(t *testing.T) {
		got := drainChildUpdates(t, &agentcoordpb.ListRunsResult{},
			[]*agentcoordpb.AgentEvent{runStarted("r1", "brave-otter", "")})
		require.Len(t, got, 1)
		assert.Equal(t, "delegated task", got[0].Text)
	})

	t.Run("an id-less start takes the harp from the snapshot", func(t *testing.T) {
		snapshot := &agentcoordpb.ListRunsResult{Runs: []*agentcoordpb.ListRunsResult_RunInfo{
			{RunId: "r1", Agent: &agentcoordpb.AgentIdentity{AgentId: "from-snapshot"}},
		}}
		got := drainChildUpdates(t, snapshot,
			[]*agentcoordpb.AgentEvent{runStarted("r1", "", "reviewer")})
		require.Len(t, got, 1)
		assert.Equal(t, "from-snapshot", got[0].Harp)
	})

	t.Run("message deltas forward as they arrive, and empty text is skipped", func(t *testing.T) {
		delta := func(text string) *agentcoordpb.AgentEvent {
			return &agentcoordpb.AgentEvent{
				RunId:   "r1",
				Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{Text: text}},
			}
		}
		got := drainChildUpdates(t, &agentcoordpb.ListRunsResult{}, []*agentcoordpb.AgentEvent{
			runStarted("r1", "brave-otter", "reviewer"), delta("one"), delta(""), delta("two"),
		})
		require.Len(t, got, 3, "the empty delta contributes nothing")
		assert.Equal(t, acpagent.ChildUpdate{Harp: "brave-otter", Kind: acpagent.ChildUpdateMessage, Text: "one"}, got[1])
		assert.Equal(t, acpagent.ChildUpdate{Harp: "brave-otter", Kind: acpagent.ChildUpdateMessage, Text: "two"}, got[2])
	})

	t.Run("completion uses the result text, or the lowercased status", func(t *testing.T) {
		completed := func(res *agentcoordpb.Result) *agentcoordpb.AgentEvent {
			return &agentcoordpb.AgentEvent{
				RunId:   "r1",
				Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{Result: res}},
			}
		}

		got := drainChildUpdates(t, &agentcoordpb.ListRunsResult{}, []*agentcoordpb.AgentEvent{
			runStarted("r1", "brave-otter", "reviewer"),
			completed(&agentcoordpb.Result{Text: "all done"}),
		})
		require.Len(t, got, 2)
		assert.Equal(t, acpagent.ChildUpdate{Harp: "brave-otter", Kind: acpagent.ChildUpdateCompleted, Text: "all done"}, got[1])

		got = drainChildUpdates(t, &agentcoordpb.ListRunsResult{}, []*agentcoordpb.AgentEvent{
			runStarted("r1", "brave-otter", "reviewer"),
			completed(&agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_FAILED}),
		})
		require.Len(t, got, 2)
		assert.Equal(t, "failed", got[1].Text, "the RUN_STATUS_ prefix is stripped and lowercased")
	})

	t.Run("a completed run's harp is forgotten, so a later delta carries none", func(t *testing.T) {
		got := drainChildUpdates(t, &agentcoordpb.ListRunsResult{}, []*agentcoordpb.AgentEvent{
			runStarted("r1", "brave-otter", "reviewer"),
			{RunId: "r1", Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{Result: &agentcoordpb.Result{Text: "done"}}}},
			{RunId: "r1", Payload: &agentcoordpb.AgentEvent_MessageDelta{MessageDelta: &agentcoordpb.MessageDelta{Text: "late"}}},
		})
		require.Len(t, got, 3)
		assert.Empty(t, got[2].Harp)
	})
}

// The other thirteen payload variants are DELIBERATELY not surfaced: an ACP
// client's child pane shows start / streamed text / completion, and nothing
// else. This pins the silence so it stays a decision rather than an omission.
func TestAdaptChildWatch_UnsurfacedVariantsEmitNothing(t *testing.T) {
	unsurfaced := []*agentcoordpb.AgentEvent{
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_StepStarted{StepStarted: &agentcoordpb.StepStarted{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_StepCompleted{StepCompleted: &agentcoordpb.StepCompleted{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_StatusChanged{StatusChanged: &agentcoordpb.StatusChanged{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_MessageStarted{MessageStarted: &agentcoordpb.MessageStarted{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_MessageCompleted{MessageCompleted: &agentcoordpb.MessageCompleted{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_ToolCallStarted{ToolCallStarted: &agentcoordpb.ToolCallStarted{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_ToolCallArgsDelta{ToolCallArgsDelta: &agentcoordpb.ToolCallArgsDelta{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentcoordpb.ToolCallCompleted{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_Interaction{Interaction: &agentcoordpb.InteractionRecorded{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_ArtifactProduced{ArtifactProduced: &agentcoordpb.ArtifactProduced{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_Summary{Summary: &agentcoordpb.Summary{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_Raw{Raw: &agentcoordpb.RawEvent{}}},
		{RunId: "r1", Payload: &agentcoordpb.AgentEvent_Custom{Custom: &agentcoordpb.CustomEvent{}}},
		{RunId: "r1", Payload: nil},
	}
	assert.Empty(t, drainChildUpdates(t, &agentcoordpb.ListRunsResult{}, unsurfaced))
}

// The adapter must return promptly when its context ends, even with events
// still queued and nobody reading — the cancellation arm of both selects.
func TestAdaptChildWatch_StopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *agentcoordpb.AgentEvent, 4)
	for i := 0; i < 4; i++ {
		events <- runStarted("r1", "brave-otter", "reviewer")
	}
	out := make(chan acpagent.ChildUpdate) // unbuffered: the send parks
	done := make(chan struct{})
	go func() {
		adaptChildWatch(ctx, &agentcoordpb.ListRunsResult{}, events, out)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("adaptChildWatch did not return after its context was cancelled")
	}
	_, open := <-out
	assert.False(t, open, "the output channel is closed on the way out")
}
