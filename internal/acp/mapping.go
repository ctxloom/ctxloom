package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/coder/acp-go-sdk"
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
// The fork's SessionUpdate union carries no discriminator field of its own
// (unlike the hand-rolled union it replaces) — exactly one variant pointer is
// non-nil after UnmarshalJSON, so the dispatch switches on THAT instead of a
// Type tag.
func mapSessionUpdate(u *api.SessionUpdate) []agent.ChatEvent {
	if u == nil {
		return nil
	}
	switch {
	case u.AgentMessageChunk != nil:
		return textEntry(agent.EntryTypeAssistant, u.AgentMessageChunk.Content)
	case u.AgentThoughtChunk != nil:
		return textEntry(agent.EntryTypeThinking, u.AgentThoughtChunk.Content)
	case u.ToolCall != nil:
		return mapToolCall(u.ToolCall)
	case u.ToolCallUpdate != nil:
		return mapToolCallUpdate(u.ToolCallUpdate)
	case u.Plan != nil:
		return mapPlan(u.Plan)
	case u.UserMessageChunk != nil:
		return nil // don't duplicate the user's own message
	default:
		return nil // unknown/unmodeled variant (e.g. current_mode_update, usage_update
		// reach here only if a caller feeds mapSessionUpdate directly without
		// going through consumeMetaUpdate first) — never crash the stream
	}
}

// textEntry emits a single content entry of the given type, or nothing when the
// content block carries no text (a content-less chunk is not surfaced — unlike a
// thinking marker, an empty ACP text chunk has nothing to show).
func textEntry(t agent.SessionEntryType, block api.ContentBlock) []agent.ChatEvent {
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
	if name == "" && tc.Kind != "" {
		name = string(tc.Kind)
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{
		Type:      agent.EntryTypeToolUse,
		ToolName:  name,
		ToolInput: rawJSON(tc.RawInput),
	}}}
}

// mapToolCallUpdate turns a tool_call_update into a tool_result entry once it has
// something to report — completed/failed status, or produced content/output.
// Bare in-progress ticks (no output) yield nothing: they're status noise, not a
// result. A failed status marks the entry as an error.
func mapToolCallUpdate(tu *api.SessionToolCallUpdate) []agent.ChatEvent {
	if tu == nil {
		return nil
	}
	var status api.ToolCallStatus
	if tu.Status != nil {
		status = *tu.Status
	}
	output := toolContentText(tu.Content)
	if output == "" {
		output = rawText(tu.RawOutput)
	}
	terminal := status == api.ToolCallStatusCompleted || status == api.ToolCallStatusFailed
	if output == "" && !terminal {
		return nil
	}
	title := ""
	if tu.Title != nil {
		title = *tu.Title
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolName:   title,
		ToolOutput: output,
		IsError:    status == api.ToolCallStatusFailed,
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
	for _, pe := range p.Entries {
		if pe.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "\n- [%s] %s", pe.Status, pe.Content)
	}
	return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: agent.EntryTypeSystem, Content: b.String()}}}
}

// --- accounting update variants ---

// usage_update is a real spec SessionUpdate variant (api.UsageUpdate) as of
// schema-v1.19.0 — H1 confirmed ctxloom's prior hand-rolled shape already
// matched it exactly, so it is now decoded straight into the real type
// instead of a bespoke usageUpdateWire. ctxloom's own session-info extension
// is NOT a spec variant (and deliberately does not collide with the spec's
// OWN session_info_update, which means something different — session
// title/timestamp metadata — as of schema-v1.19.0; see the emitter's doc
// comment in internal/acpagent/wire.go for the L0 checklist's B3 decision),
// so it stays hand-decoded under its own ctxloom-scoped discriminator. Both
// are intercepted here, before the strict api.SessionUpdate union ever sees
// them, because they feed turn ACCOUNTING rather than an emitted entry —
// protocol v1 itself carries no token/cost/context-window/timing fields
// anywhere else (PromptResponse is stopReason alone), so this is the ONLY
// usage data any ACP agent delivers today.
const (
	usageUpdateVariant = "usage_update"
	sessionInfoVariant = "ctxloom_session_info"
)

// sessionInfoWire is the ctxloom_session_info subset the turn accounting
// consumes (the frame also carries permissionMode/mcpServers).
type sessionInfoWire struct {
	Model         string `json:"model"`
	ContextWindow int    `json:"contextWindow"`
}

// updateDiscriminator reads a raw update's sessionUpdate type tag without
// decoding the full variant. Malformed JSON reads as "" (not ours to handle).
func updateDiscriminator(raw json.RawMessage) string {
	var d struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.SessionUpdate
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

// permissionRequestEvent maps an inbound session/request_permission onto the
// backend-agnostic forwarded-permission event: the tool's human-readable title,
// its raw input, and the offered options verbatim (the upstream decider needs
// the real option ids to answer with).
func permissionRequestEvent(id string, req *api.RequestPermissionRequest) *agent.PermissionRequest {
	p := &agent.PermissionRequest{ID: id, ToolInput: rawJSON(req.ToolCall.RawInput), Kind: toolKindString(req.ToolCall.Kind)}
	if req.ToolCall.Title != nil {
		p.ToolName = *req.ToolCall.Title
	}
	if p.ToolName == "" {
		p.ToolName = toolKindString(req.ToolCall.Kind)
	}
	for _, o := range req.Options {
		p.Options = append(p.Options, agent.PermissionOption{ID: string(o.OptionId), Kind: string(o.Kind), Name: o.Name})
	}
	return p
}

// decidePermission answers a tool-call permission request. It mirrors how the
// claude driver handles permissions: allow only under a bypass posture, otherwise
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
// ContentBlock is a plain value (not a pointer) in the fork's generated
// shape, and carries no discriminator field — it holds exactly one non-nil
// variant pointer after decode.
func contentBlockText(block api.ContentBlock) string {
	if block.Text != nil {
		return block.Text.Text
	}
	return ""
}

// toolContentText flattens a tool call's content collection (each element a
// ToolCallContent union: content block / diff / terminal reference) into
// text. Content is now PROPERLY TYPED as []api.ToolCallContent (the pinned
// SDK left this []interface{}/interface{}, requiring a remarshal-through-JSON
// dance this function used to do for every element — see the old module's
// unions_generated.go and this file's prior revision). The union carries no
// discriminator field of its own — dispatch switches on which variant
// pointer is non-nil.
func toolContentText(content []api.ToolCallContent) string {
	var parts []string
	for _, tcc := range content {
		switch {
		case tcc.Content != nil:
			if t := contentBlockText(tcc.Content.Content); t != "" {
				parts = append(parts, t)
			}
		case tcc.Diff != nil:
			parts = append(parts, tcc.Diff.Path+"\n"+tcc.Diff.NewText)
		}
	}
	return strings.Join(parts, "\n")
}

// --- loosely-typed field helpers (rawInput/rawOutput carry no schema type —
// the spec leaves them arbitrary JSON — so these coerce them defensively) ---

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

// toolKindString unwraps an optional tool kind for display, or "" when unset.
func toolKindString(k *api.ToolKind) string {
	if k == nil {
		return ""
	}
	return string(*k)
}
