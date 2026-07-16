package acpagent

import (
	"encoding/json"
	"strconv"

	api "github.com/coder/acp-go-sdk"

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
//	EntryTypeSystem     → agent_message_chunk (Q1 fallback: the IR has already
//	                                           flattened structured content —
//	                                           e.g. a plan's entries — into
//	                                           e.Content by the time it reaches
//	                                           here, see internal/acp/mapping.go's
//	                                           mapPlan; there is no dedicated ACP
//	                                           `plan` update to rebuild from a
//	                                           string, so this at least makes the
//	                                           content VISIBLE instead of vanishing)
//	ChatSessionInfo     → session_info_update (model/mcp header; see sessionInfoUpdateWire)
//	TurnMeta (Complete) → usage_update (context gauge + cost; see usageUpdateWire) then ends the turn

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
			AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{Content: textBlock(e.Content)},
		}}
	case agent.EntryTypeThinking:
		return []api.SessionUpdate{{
			AgentThoughtChunk: &api.SessionUpdateAgentThoughtChunk{Content: textBlock(e.Content)},
		}}
	case agent.EntryTypeToolUse:
		id := sess.pushToolCall(e.ToolName)
		return []api.SessionUpdate{{
			ToolCall: &api.SessionUpdateToolCall{
				ToolCallId: id,
				Title:      e.ToolName,
				Status:     api.ToolCallStatusInProgress,
				RawInput:   rawValue(e.ToolInput),
			},
		}}
	case agent.EntryTypeToolResult:
		id := sess.popToolCall(e.ToolName)
		status := api.ToolCallStatusCompleted
		if e.IsError {
			status = api.ToolCallStatusFailed
		}
		return []api.SessionUpdate{{
			ToolCallUpdate: &api.SessionToolCallUpdate{
				ToolCallId: id,
				Status:     &status,
				RawOutput:  e.ToolOutput,
			},
		}}
	case agent.EntryTypeSystem:
		if e.Content == "" {
			return nil
		}
		return []api.SessionUpdate{{
			AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{Content: textBlock(e.Content)},
		}}
	default:
		return nil
	}
}

// replayEntry maps one RECORDED session entry onto session/update
// notifications for session/load replay. Unlike the live mapping, user entries
// ARE emitted (the spec's replay includes the user's messages); everything else
// (including system entries, per mapEvent's Q1 fallback above) replays exactly
// as it was live mapped.
func (sess *session) replayEntry(e agent.SessionEntry) []api.SessionUpdate {
	if e.Type == agent.EntryTypeUser {
		if e.Content == "" {
			return nil
		}
		return []api.SessionUpdate{{
			UserMessageChunk: &api.SessionUpdateUserMessageChunk{Content: textBlock(e.Content)},
		}}
	}
	return sess.mapEvent(agent.ChatEvent{Entry: &e})
}

// permissionRequestWire renders a forwarded engine permission request as the
// outbound session/request_permission body. The referenced toolCall reuses the
// most recent OPEN generated id for the same tool name — the engine announces
// the tool_call just before asking permission for it — falling back to a fresh
// id when none is open (a lone toolCallId is still valid ACP).
//
// L0 checklist A2: Options is built from make(..., 0, ...) rather than a
// `var` (nil) slice, so a permission request with ZERO engine-supplied
// options still marshals `"options":[]`, not `"options":null` — the current
// spec's RequestPermissionRequest requires options to be an array (no null
// alternative); a nil slice through encoding/json is indistinguishable from
// one that was never populated, and used to marshal as `null`.
func (sess *session) permissionRequestWire(p *agent.PermissionRequest) api.RequestPermissionRequest {
	req := api.RequestPermissionRequest{
		SessionId: sess.id,
		ToolCall:  api.ToolCallUpdate{ToolCallId: sess.peekToolCall(p.ToolName)},
		Options:   make([]api.PermissionOption, 0, len(p.Options)),
	}
	if p.ToolName != "" {
		title := p.ToolName
		req.ToolCall.Title = &title
	}
	if v := rawValue(p.ToolInput); v != nil {
		req.ToolCall.RawInput = v
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, api.PermissionOption{
			OptionId: api.PermissionOptionId(o.ID),
			Kind:     api.PermissionOptionKind(o.Kind),
			Name:     o.Name,
		})
	}
	return req
}

// peekToolCall returns the NEWEST open call id for name without consuming it
// (the result will pop it later), or a fresh id when none is open.
func (sess *session) peekToolCall(name string) api.ToolCallId {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if ids := sess.openCall[name]; len(ids) > 0 {
		return ids[len(ids)-1]
	}
	sess.toolSeq++
	return api.ToolCallId("call-" + strconv.FormatInt(sess.toolSeq, 10))
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

// sessionInfoUpdateWire projects one ChatSessionInfo (the engine's one-time
// start-of-chat metadata) onto ctxloom's own (renamed, non-colliding)
// session-info session/update, so a client can render a model/mcp header.
// Returns nil when there is nothing worth surfacing (so an empty
// ChatSessionInfo emits no notification). See ctxloomSessionInfoUpdate's doc
// comment (wire.go) for why this is NOT the spec's session_info_update.
func sessionInfoUpdateWire(info *agent.ChatSessionInfo) any {
	if info == nil || (info.Model == "" && info.PermissionMode == "" && info.ContextWindow == 0 && len(info.MCPServers) == 0) {
		return nil
	}
	u := ctxloomSessionInfoUpdate{
		SessionUpdate:  "ctxloom_session_info",
		Model:          info.Model,
		PermissionMode: info.PermissionMode,
		ContextWindow:  info.ContextWindow,
	}
	for _, m := range info.MCPServers {
		u.McpServers = append(u.McpServers, mcpStatusWire{Name: m.Name, Status: m.Status})
	}
	return u
}

// usageUpdateWire projects one turn's completion accounting (TurnMeta) onto
// the real usage_update SessionUpdate variant (api.UsageUpdate/api.Cost) —
// the context-window gauge and cumulative cost. H1 confirmed this hand-rolled
// shape already matched the current spec exactly, so it is now built through
// api.SessionUpdate directly rather than a bespoke usageUpdate/usageCost pair.
// `used`/`size` follow ctxloom's own gauge convention (InputTokens against the
// model's ContextWindow; see run_structured.go), CostUSD is reported in USD.
// Returns nil when the turn carried no accounting worth reporting (so a bare
// completion — e.g. a cancel — emits no gauge). NOTE: returns the
// api.SessionUpdate by VALUE (never a nil *api.SessionUpdate) specifically so
// the "nothing to report" case can return a plain untyped nil `any` —
// emitUpdate's `update == nil` no-op check would otherwise be defeated by a
// typed-nil-in-interface (a non-nil interface wrapping a nil *SessionUpdate).
func usageUpdateWire(c *agent.TurnMeta) any {
	if c == nil || (c.InputTokens == 0 && c.ContextWindow == 0 && c.CostUSD == 0) {
		return nil
	}
	u := api.SessionUsageUpdate{Used: c.InputTokens, Size: c.ContextWindow}
	if c.CostUSD != 0 {
		u.Cost = &api.Cost{Amount: c.CostUSD, Currency: "USD"}
	}
	return api.SessionUpdate{UsageUpdate: &u}
}

// textBlock wraps text in an ACP text content block.
func textBlock(text string) api.ContentBlock {
	return api.TextBlock(text)
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
