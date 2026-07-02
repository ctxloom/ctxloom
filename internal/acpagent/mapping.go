package acpagent

import (
	"encoding/json"
	"strconv"

	"github.com/joshgarnett/agent-client-protocol-go/acp/api"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the INVERSE of internal/acp/mapping.go: it maps ctxloom's
// backend-agnostic chat entries onto ACP `session/update` notifications for
// the outer client.
//
//	EntryTypeAssistant  → agent_message_chunk
//	EntryTypeThinking   → agent_thought_chunk
//	EntryTypeToolUse    → tool_call        (generated toolCallId, pushed per tool name)
//	EntryTypeToolResult → tool_call_update (pops the matching open id; failed → status failed)
//	EntryTypeUser       → (dropped — never echo the user's message back)
//	EntryTypeSystem     → (dropped — structural, not conversation content)
//	ChatSessionInfo     → (dropped — no wire projection in slice 1)

// mapEvent converts one chat event into 0..1 session updates, tracking
// tool-call id pairing on the session.
func (sess *session) mapEvent(ev agent.ChatEvent) []api.SessionUpdate {
	if ev.Entry == nil {
		return nil
	}
	e := ev.Entry
	switch e.Type {
	case agent.EntryTypeAssistant:
		return []api.SessionUpdate{{
			Type:              api.SessionUpdateTypeAgentMessageChunk,
			AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{Content: textBlock(e.Content)},
		}}
	case agent.EntryTypeThinking:
		return []api.SessionUpdate{{
			Type:              api.SessionUpdateTypeAgentThoughtChunk,
			AgentThoughtChunk: &api.SessionUpdateAgentThoughtChunk{Content: textBlock(e.Content)},
		}}
	case agent.EntryTypeToolUse:
		id := sess.pushToolCall(e.ToolName)
		status := api.ToolCallStatusInProgress
		return []api.SessionUpdate{{
			Type: api.SessionUpdateTypeToolCall,
			ToolCall: &api.SessionUpdateToolCall{
				Toolcallid: &id,
				Title:      e.ToolName,
				Status:     &status,
				Rawinput:   rawValue(e.ToolInput),
			},
		}}
	case agent.EntryTypeToolResult:
		id := sess.popToolCall(e.ToolName)
		status := api.ToolCallStatusCompleted
		if e.IsError {
			status = api.ToolCallStatusFailed
		}
		return []api.SessionUpdate{{
			Type: api.SessionUpdateTypeToolCallUpdate,
			ToolCallUpdate: &api.SessionUpdateToolCallUpdate{
				Toolcallid: &id,
				Status:     status,
				Rawoutput:  e.ToolOutput,
			},
		}}
	default:
		return nil
	}
}

// pushToolCall generates a fresh toolCallId and records it as the open call
// for name, so the eventual result can target it.
func (sess *session) pushToolCall(name string) api.ToolCallId {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.toolSeq++
	id := api.ToolCallId("call-" + strconv.FormatInt(sess.toolSeq, 10))
	sess.openCall[name] = append(sess.openCall[name], id)
	return id
}

// popToolCall pairs a result with the OLDEST open call of the same tool name
// (FIFO — engines report results in call order). An unmatched result gets a
// fresh id: a lone tool_call_update is still valid ACP, and honesty beats
// mis-pairing.
func (sess *session) popToolCall(name string) api.ToolCallId {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if ids := sess.openCall[name]; len(ids) > 0 {
		id := ids[0]
		sess.openCall[name] = ids[1:]
		return id
	}
	sess.toolSeq++
	return api.ToolCallId("call-" + strconv.FormatInt(sess.toolSeq, 10))
}

// textBlock wraps text in an ACP text content block.
func textBlock(text string) *api.ContentBlock {
	return &api.ContentBlock{Type: api.ContentBlockTypeText, Text: &api.ContentBlockText{Text: text}}
}

// rawValue decodes raw JSON to a plain value for the loosely-typed rawInput
// field (nil stays nil so omitempty drops it).
func rawValue(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}
