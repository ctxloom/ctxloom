package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/joshgarnett/agent-client-protocol-go/acp/api"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the CORE of the ACP backend: it maps ACP `session/update`
// notifications onto ctxloom's backend-agnostic chat entries. All ACP-specific
// wire knowledge about the update stream lives here (the polymorphic design —
// mirroring internal/claude/chat_stream.go); the session driver and ctxloom only
// ever see agent.ChatEvent.
//
// ACP → ctxloom entry mapping:
//
//	agent_message_chunk   → EntryTypeAssistant   (streamed assistant text)
//	agent_thought_chunk   → EntryTypeThinking    (summarized reasoning — the win:
//	                                               ACP surfaces thinking prose that
//	                                               claude-code's stream-json strips)
//	tool_call             → EntryTypeToolUse      (title/kind → ToolName, rawInput → ToolInput)
//	tool_call_update      → EntryTypeToolResult   (only once it carries output or a
//	                                               terminal status; failed → IsError)
//	plan                  → EntryTypeSystem        (rendered checklist; no dedicated
//	                                               plan entry type, System is the best fit)
//	user_message_chunk    → (dropped — never echo the user's own message back)
//	unknown / malformed   → (dropped — the stream must never crash on a frame we
//	                         don't model)
//
// Each update yields 0..1 entries. Chunks are emitted one entry apiece (a frontend
// concatenates), matching claude-code's per-block emission.

// mapSessionUpdate normalizes one decoded session/update into 0..1 ChatEvents.
func mapSessionUpdate(u *api.SessionUpdate) []agent.ChatEvent {
	if u == nil {
		return nil
	}
	switch u.Type {
	case api.SessionUpdateTypeAgentMessageChunk:
		if u.AgentMessageChunk == nil {
			return nil
		}
		return textEntry(agent.EntryTypeAssistant, u.AgentMessageChunk.Content)
	case api.SessionUpdateTypeAgentThoughtChunk:
		if u.AgentThoughtChunk == nil {
			return nil
		}
		return textEntry(agent.EntryTypeThinking, u.AgentThoughtChunk.Content)
	case api.SessionUpdateTypeToolCall:
		return mapToolCall(u.ToolCall)
	case api.SessionUpdateTypeToolCallUpdate:
		return mapToolCallUpdate(u.ToolCallUpdate)
	case api.SessionUpdateTypePlan:
		return mapPlan(u.Plan)
	case api.SessionUpdateTypeUserMessageChunk:
		return nil // don't duplicate the user's own message
	default:
		return nil // unknown variant — never crash the stream
	}
}

// textEntry emits a single content entry of the given type, or nothing when the
// content block carries no text (a content-less chunk is not surfaced — unlike a
// thinking marker, an empty ACP text chunk has nothing to show).
func textEntry(t agent.SessionEntryType, block *api.ContentBlock) []agent.ChatEvent {
	text := contentBlockText(block)
	if text == "" {
		return nil
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: t, Content: text}}}
}

// mapToolCall turns a new tool_call into a tool_use entry: the human-readable
// title (falling back to the tool kind) names the tool, and rawInput carries the
// arguments.
func mapToolCall(tc *api.SessionUpdateToolCall) []agent.ChatEvent {
	if tc == nil {
		return nil
	}
	name := tc.Title
	if name == "" && tc.Kind != nil {
		name = string(*tc.Kind)
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{
		Type:      agent.EntryTypeToolUse,
		ToolName:  name,
		ToolInput: rawJSON(tc.Rawinput),
	}}}
}

// mapToolCallUpdate turns a tool_call_update into a tool_result entry once it has
// something to report — completed/failed status, or produced content/output.
// Bare in-progress ticks (no output) yield nothing: they're status noise, not a
// result. A failed status marks the entry as an error.
func mapToolCallUpdate(tu *api.SessionUpdateToolCallUpdate) []agent.ChatEvent {
	if tu == nil {
		return nil
	}
	status := statusString(tu.Status)
	output := toolContentText(tu.Content)
	if output == "" {
		output = rawText(tu.Rawoutput)
	}
	terminal := status == string(api.ToolCallStatusCompleted) || status == string(api.ToolCallStatusFailed)
	if output == "" && !terminal {
		return nil
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolName:   titleString(tu.Title),
		ToolOutput: output,
		IsError:    status == string(api.ToolCallStatusFailed),
	}}}
}

// mapPlan renders the agent's execution plan as a single system entry — a
// checklist a frontend can display. There is no dedicated plan entry type; System
// (structural, non-conversational, not a tool call) is the closest fit.
func mapPlan(p *api.SessionUpdatePlan) []agent.ChatEvent {
	if p == nil || len(p.Entries) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("Plan:")
	for _, raw := range p.Entries {
		var pe api.PlanEntry
		if remarshal(raw, &pe) != nil || pe.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "\n- [%s] %s", pe.Status, pe.Content)
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: agent.EntryTypeSystem, Content: b.String()}}}
}

// --- permission decisioning ---

// ACP RequestPermissionResponse.outcome discriminator values.
const (
	outcomeSelected  = "selected"
	outcomeCancelled = "cancelled"
)

// permissionOutcome is the inner {outcome, optionId} object of a permission
// response. optionId is present only for a "selected" outcome.
type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionId string `json:"optionId,omitempty"`
}

// permissionResult is the full session/request_permission response body:
// {"outcome": {"outcome": "selected"|"cancelled", "optionId"?: ...}}.
type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

// decidePermission answers a tool-call permission request. It mirrors how the
// claude driver handles permissions: allow only under AutoApprove, otherwise
// reject. When allowing it selects an allow_* option; when rejecting it selects a
// reject_* option; if the agent offered no option of the needed kind it falls
// back to a "cancelled" outcome (a safe no-op that neither approves nor commits a
// remembered rejection).
func decidePermission(options []api.PermissionOption, allow bool) permissionResult {
	if id := pickOption(options, allow); id != "" {
		return permissionResult{Outcome: permissionOutcome{Outcome: outcomeSelected, OptionId: id}}
	}
	return permissionResult{Outcome: permissionOutcome{Outcome: outcomeCancelled}}
}

// pickOption returns the id of the first option matching the desired direction,
// preferring the one-shot kind over the remembered ("always") kind.
func pickOption(options []api.PermissionOption, allow bool) string {
	var want []api.PermissionOptionKind
	if allow {
		want = []api.PermissionOptionKind{api.PermissionOptionKindAllowOnce, api.PermissionOptionKindAllowAlways}
	} else {
		want = []api.PermissionOptionKind{api.PermissionOptionKindRejectOnce, api.PermissionOptionKindRejectAlways}
	}
	for _, k := range want {
		for _, o := range options {
			if o.Kind == k {
				return string(o.OptionId)
			}
		}
	}
	return ""
}

// --- content-block flattening ---

// contentBlockText extracts the plain text from a content block, ignoring
// non-text variants (image/audio/resource) which have no text projection.
func contentBlockText(block *api.ContentBlock) string {
	if block == nil {
		return ""
	}
	if block.Type == api.ContentBlockTypeText && block.Text != nil {
		return block.Text.Text
	}
	return ""
}

// toolContentText flattens a tool call's content collection (each element a
// ToolCallContent union: content block / diff / terminal) into text. The wire
// type is loose — []interface{} on a tool_call, bare interface{} on a
// tool_call_update — so coerce to a slice of raw elements and re-marshal each
// into the union.
func toolContentText(content interface{}) string {
	elems, ok := content.([]interface{})
	if !ok {
		return ""
	}
	var parts []string
	for _, raw := range elems {
		var tcc api.ToolCallContent
		if remarshal(raw, &tcc) != nil {
			continue
		}
		switch tcc.Type {
		case api.ToolCallContentTypeContent:
			if tcc.Content != nil {
				if t := contentBlockText(tcc.Content.Content); t != "" {
					parts = append(parts, t)
				}
			}
		case api.ToolCallContentTypeDiff:
			if tcc.Diff != nil {
				parts = append(parts, tcc.Diff.Path+"\n"+tcc.Diff.Newtext)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// --- loosely-typed field helpers (the SDK types several update fields as
// interface{}; these coerce them back defensively) ---

// rawJSON re-marshals an arbitrary decoded value to raw JSON for ToolInput.
func rawJSON(v interface{}) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// rawText renders a raw value as text: a JSON string unwraps to its value, other
// shapes stringify as compact JSON.
func rawText(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func statusString(v interface{}) string { return rawText(v) }

func titleString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if p, ok := v.(*string); ok && p != nil {
		return *p
	}
	return ""
}

// remarshal round-trips a decoded interface{} value through JSON into a typed
// destination — the SDK leaves several nested update fields as interface{}
// (decoded to map[string]interface{}), so this recovers the typed view.
func remarshal(v, dst interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
