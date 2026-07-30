package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// eventScriptChat replays a fixed ChatEvent script and ends its stream. It
// drives adapt directly, without a turn loop, so the message/tool lifecycle can
// be exercised at its edges.
type eventScriptChat struct{ script []agent.ChatEvent }

func (e *eventScriptChat) Chat(ctx context.Context, _ agent.ChatRequest, _ <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	for _, ev := range e.script {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// TestEngineHost_Adapt_MessageAndToolLifecycle characterizes the native-event
// adaptation at the edges the CCN-10 split of adapt (U021-F15) moves: message
// coalescing by contiguous type, the type change that closes an open message,
// a tool call closing one too, a tool RESULT with no matching start, and the
// empty-content entry that is skipped outright.
func TestEngineHost_Adapt_MessageAndToolLifecycle(t *testing.T) {
	entry := func(tp agent.SessionEntryType, content string) agent.ChatEvent {
		return agent.ChatEvent{Entry: &agent.SessionEntry{Type: tp, Content: content}}
	}
	home := &fakeEngineHome{}
	sc := &eventScriptChat{script: []agent.ChatEvent{
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

	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return len(home.exited) == 1
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

	// The turn boundary and the terminal result.
	assert.Equal(t, []string{CustomTurnStarted, CustomTurnIdle}, home.customNamesLocked())
	var completed *agentcoordpb.RunCompleted
	for _, ev := range home.events {
		if rc := ev.GetRunCompleted(); rc != nil {
			completed = rc
		}
	}
	require.NotNil(t, completed)
	assert.Equal(t, agentcoordpb.Result_RUN_STATUS_SUCCEEDED, completed.GetResult().GetStatus())
	assert.Equal(t, uint32(1), completed.GetResult().GetNumTurns())
	assert.Equal(t, uint64(3), completed.GetResult().GetUsage().GetOutputTokens())
	assert.Equal(t, 0, home.exited[0].Code)
}
