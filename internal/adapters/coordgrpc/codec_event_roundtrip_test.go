package coordgrpc

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestEvent_RoundTripsEveryPayloadVariant pins the plane-1 event codec: every
// payload variant EventToWire encodes lands in its own oneof wrapper, and
// EventFromWire decodes it back to the same event, envelope included.
func TestEvent_RoundTripsEveryPayloadVariant(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		payload coord.EventPayload
		wire    any
	}{
		{coord.RunStarted{Input: map[string]any{"task": "x"}, Agent: &coord.AgentIdentity{AgentID: "a", Harness: "claude"}, Config: map[string]any{"k": "v"}, ParentRunID: "p-1"}, &agentcoordpb.AgentEvent_RunStarted{}},
		{coord.StepStarted{StepID: "s", Title: "t", Ordinal: 2}, &agentcoordpb.AgentEvent_StepStarted{}},
		{coord.StepCompleted{StepID: "s", Outcome: coord.StepOutcomeFailed, Detail: "d"}, &agentcoordpb.AgentEvent_StepCompleted{}},
		{coord.StatusChanged{Phase: coord.PhasePaused, Detail: "d"}, &agentcoordpb.AgentEvent_StatusChanged{}},
		{coord.InteractionRecorded{RequestID: "r", Kind: "approval", Resolution: coord.ResolutionDenied, Detail: map[string]any{"why": "no"}}, &agentcoordpb.AgentEvent_Interaction{}},
		{coord.RunCompleted{Result: &coord.Result{Status: coord.RunStatusFailed, Text: "done", NumTurns: 3}}, &agentcoordpb.AgentEvent_RunCompleted{}},
		{coord.MessageStarted{MessageID: "m", Role: coord.RoleTool, Channel: coord.ChannelReasoning}, &agentcoordpb.AgentEvent_MessageStarted{}},
		{coord.MessageDelta{MessageID: "m", Text: "hi"}, &agentcoordpb.AgentEvent_MessageDelta{}},
		{coord.MessageCompleted{MessageID: "m", FullText: "hi there"}, &agentcoordpb.AgentEvent_MessageCompleted{}},
		{coord.ToolCallStarted{ToolCallID: "c", ToolName: "grep"}, &agentcoordpb.AgentEvent_ToolCallStarted{}},
		{coord.ToolCallArgsDelta{ToolCallID: "c", ArgsJSONFragment: `{"q":`}, &agentcoordpb.AgentEvent_ToolCallArgsDelta{}},
		{coord.ToolCallCompleted{ToolCallID: "c", Args: map[string]any{"q": "x"}, IsError: true, ResultText: "r", ArtifactIDs: []string{"a1"}, Elapsed: 1500 * time.Millisecond}, &agentcoordpb.AgentEvent_ToolCallCompleted{}},
		{coord.ArtifactProduced{ArtifactID: "a1", Kind: coord.ArtifactKindCodeDiff, Name: "n", MediaType: "text/plain", SizeBytes: 4}, &agentcoordpb.AgentEvent_ArtifactProduced{}},
		{coord.Summary{Scope: coord.ScopeStep, StepID: "s", Text: "sum", CoversThroughSeq: 9, ArtifactIDs: []string{"a1"}}, &agentcoordpb.AgentEvent_Summary{}},
		{coord.EventsLost{Lost: []coord.LostRange{{RunID: "r", FirstSeq: 1, LastSeq: 4}}}, &agentcoordpb.AgentEvent_EventsLost{}},
		{coord.RawEvent{Source: "claude", Event: map[string]any{"type": "x"}}, &agentcoordpb.AgentEvent_Raw{}},
		{coord.CustomEvent{Name: "n", Value: map[string]any{"v": "1"}}, &agentcoordpb.AgentEvent_Custom{}},
		{nil, nil},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%T", tc.payload), func(t *testing.T) {
			e := coord.Event{TaskID: "task", RunID: "run", Seq: 7, OccurredAt: at, TurnID: "turn", ParentItemID: "item", Traceparent: "tp", Payload: tc.payload}
			wire := EventToWire(e)
			assert.IsType(t, tc.wire, wire.GetPayload())
			assert.Equal(t, e, EventFromWire(wire))
		})
	}
}

// TestResult_ExitCodeRoundTripsPresence pins the engine exit status's
// presence, not only its value: an unset code means no engine exit produced
// the result, and a set zero is an engine that exited cleanly — the two must
// not collapse into each other on the wire.
func TestResult_ExitCodeRoundTripsPresence(t *testing.T) {
	code := func(v int32) *int32 { return &v }
	for _, tc := range []struct {
		name string
		exit *int32
	}{
		{"unset", nil},
		{"zero", code(0)},
		{"engine code", code(3)},
		{"signal", code(143)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &coord.Result{Status: coord.RunStatusFailed, ExitCode: tc.exit}
			wire := resultToWire(in)
			if tc.exit == nil {
				assert.Nil(t, wire.ExitCode, "an unset code stays unset on the wire")
			} else if assert.NotNil(t, wire.ExitCode, "a set code is present on the wire") {
				assert.Equal(t, *tc.exit, *wire.ExitCode)
			}
			assert.Equal(t, in, resultFromWire(wire))
		})
	}
}
