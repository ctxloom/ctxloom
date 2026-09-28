package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestEngineHost_Adapt_MessageAndToolLifecycle characterizes the native-event
// adaptation at the edges the CCN-10 split of adapt moves: message
// coalescing by contiguous type, the type change that closes an open message,
// a tool call closing one too, a tool RESULT with no matching start, and the
// empty-content entry that is skipped outright.
func TestEngineHost_Adapt_MessageAndToolLifecycle(t *testing.T) {
	entry := func(tp agent.SessionEntryType, content string) agent.ChatEvent {
		return agent.ChatEvent{Entry: &agent.SessionEntry{Type: tp, Content: content}}
	}
	home := &fakeEngineHome{}
	sc := &eventScript{script: []agent.ChatEvent{
		entry(agent.EntryTypeAssistant, "one"),
		entry(agent.EntryTypeAssistant, " and two"),                                         // same type: same message
		entry(agent.EntryTypeAssistant, ""),                                                 // empty: skipped entirely
		entry(agent.EntryTypeThinking, "hmm"),                                               // type change: closes the open one
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolOutput: "orphan"}}, // no start
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "grep", ToolInput: []byte(`{"q":"x"}`)}},
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolOutput: "hit", IsError: true}},
		entry(agent.EntryTypeSystem, "note"),
		{Complete: &agent.TurnMeta{StopReason: "end_turn", OutputTokens: 3}},
	}}

	eh := newTestEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := handleBounded(t, eh, &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	// The turn's boundary is the barrier: the host parks once the turn's
	// process ended and everything it relayed has been adapted.
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == coord.CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	home.mu.Lock()
	defer home.mu.Unlock()

	type seen struct {
		kind string
		id   string
		text string
	}
	var got []seen
	for _, ev := range home.events {
		switch p := ev.GetPayload().(type) {
		case *agentcoordpb.AgentEvent_MessageStarted:
			got = append(got, seen{"started", p.MessageStarted.GetMessageId(), p.MessageStarted.GetRole().String() + "/" + p.MessageStarted.GetChannel().String()})
		case *agentcoordpb.AgentEvent_MessageDelta:
			got = append(got, seen{"delta", p.MessageDelta.GetMessageId(), p.MessageDelta.GetText()})
		case *agentcoordpb.AgentEvent_MessageCompleted:
			got = append(got, seen{"completed", p.MessageCompleted.GetMessageId(), ""})
		case *agentcoordpb.AgentEvent_ToolCallStarted:
			got = append(got, seen{"tool_started", p.ToolCallStarted.GetToolCallId(), p.ToolCallStarted.GetToolName()})
		case *agentcoordpb.AgentEvent_ToolCallArgsDelta:
			got = append(got, seen{"tool_args", p.ToolCallArgsDelta.GetToolCallId(), p.ToolCallArgsDelta.GetArgsJsonFragment()})
		case *agentcoordpb.AgentEvent_ToolCallCompleted:
			got = append(got, seen{"tool_completed", p.ToolCallCompleted.GetToolCallId(), p.ToolCallCompleted.GetResultText()})
		}
	}

	assert.Equal(t, []seen{
		{"started", "m-1", "MESSAGE_ROLE_ASSISTANT/MESSAGE_CHANNEL_FINAL"},
		{"delta", "m-1", "one"},
		{"delta", "m-1", " and two"},
		{"completed", "m-1", ""},
		{"started", "m-2", "MESSAGE_ROLE_ASSISTANT/MESSAGE_CHANNEL_REASONING"},
		{"delta", "m-2", "hmm"},
		{"completed", "m-2", ""},
		{"tool_completed", "tc-unpaired-1", "orphan"},
		{"tool_started", "tc-2", "grep"},
		{"tool_args", "tc-2", `{"q":"x"}`},
		{"tool_completed", "tc-2", "hit"},
		{"started", "m-3", "MESSAGE_ROLE_SYSTEM/MESSAGE_CHANNEL_LOG"},
		{"delta", "m-3", "note"},
		{"completed", "m-3", ""},
	}, got)

	// The turn boundary; no terminal — the host parks for the next turn.
	assert.Equal(t, []string{coord.CustomTurnStarted, coord.CustomTurnIdle}, home.customNamesLocked())
	assert.Empty(t, home.exited, "a clean boundary is not the run's end")
}
