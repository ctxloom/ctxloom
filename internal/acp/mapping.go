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
//	tool_call             → EntryTypeToolUse      (title/kind → ToolName, rawInput →
//	                                               ToolInput, toolCallId → ToolCallID,
//	                                               kind → ToolKind, locations →
//	                                               ToolLocations)
//	tool_call_update      → EntryTypeToolResult   (only once it carries output or a
//	                                               terminal status; failed → IsError;
//	                                               same richness as tool_call, plus
//	                                               content → ToolContent structurally)
//	plan                  → EntryTypeSystem        (SystemKind=SystemKindPlan; Plan
//	                                               carries the STRUCTURED entries —
//	                                               the fix for the conformance
//	                                               audit's headline finding: a prior
//	                                               revision flattened this into
//	                                               Content alone, which the outbound
//	                                               side (acpagent/mapping.go) had no
//	                                               way to turn back into a real ACP
//	                                               `plan` update. Content still
//	                                               carries the rendered checklist as
//	                                               a fallback for any consumer that
//	                                               only reads text.)
//	available_commands_update,
//	current_mode_update    → ChatEvent{Raw: ...}  (no IR entry type of their
//	                                               own, forwarded on the curated raw
//	                                               passthrough allowlist instead of
//	                                               being silently dropped — see
//	                                               ChatEvent.Raw's doc comment)
//	user_message_chunk    → (dropped — never echo the user's own message back)
//	unknown / malformed   → (dropped — the stream must never crash on a frame we
//	                         don't model)
//
// Additionally, ANY of the variants above that carries a non-empty ACP `_meta`
// object attaches it to the emitted ChatEvent's Raw field (the third
// allowlist entry) — see metaRaw below.
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
		return textEntry(agent.EntryTypeAssistant, u.AgentMessageChunk.Content, u.AgentMessageChunk.Meta)
	case u.AgentThoughtChunk != nil:
		return textEntry(agent.EntryTypeThinking, u.AgentThoughtChunk.Content, u.AgentThoughtChunk.Meta)
	case u.ToolCall != nil:
		return mapToolCall(u.ToolCall)
	case u.ToolCallUpdate != nil:
		return mapToolCallUpdate(u.ToolCallUpdate)
	case u.Plan != nil:
		return mapPlan(u.Plan)
	case u.UserMessageChunk != nil:
		return nil // don't duplicate the user's own message
	case u.AvailableCommandsUpdate != nil, u.CurrentModeUpdate != nil:
		// No IR entry type exists for these (ctxloom itself doesn't
		// consume/mediate them), but they're on the curated raw-passthrough
		// allowlist rather than silently dropped like a true unknown variant.
		// api.SessionUpdate.MarshalJSON reconstructs the properly-tagged
		// `{"sessionUpdate":"...", ...}` object from whichever pointer is
		// set, so re-marshaling `u` here round-trips exactly what the engine
		// sent (this variant only) without needing the ORIGINAL raw bytes at
		// all.
		return rawOnlyEvent(u)
	default:
		return nil // unknown/unmodeled variant (e.g. usage_update — reaches
		// here only if a caller feeds mapSessionUpdate directly without going
		// through consumeMetaUpdate first) — never crash the stream
	}
}

// rawOnlyEvent marshals u (expected to have exactly one of the allowlisted
// variants set) into a raw-only ChatEvent — Entry/Complete/Session/Permission
// all nil, only Raw populated. A marshal failure drops the frame rather than
// panicking or fabricating a malformed one, and says so: the drop is otherwise
// indistinguishable from an update the engine never sent, which is why every
// analogous decode failure in session.go warns too.
func rawOnlyEvent(u *api.SessionUpdate) []agent.ChatEvent {
	b, err := json.Marshal(u)
	if err != nil {
		warnf("acp: dropping unmarshalable session/update passthrough frame: %v", err)
		return nil
	}
	return []agent.ChatEvent{{Raw: b}}
}

// metaRaw wraps a variant's `_meta` object (when non-empty) as the small Raw
// envelope `{"_meta": ...}` the allowlist forwards it as. Returns nil for an
// empty/absent meta so a ChatEvent with nothing else to report doesn't
// acquire an empty, meaningless Raw field.
func metaRaw(meta map[string]any) json.RawMessage {
	if len(meta) == 0 {
		return nil
	}
	b, err := json.Marshal(struct {
		Meta map[string]any `json:"_meta"`
	}{Meta: meta})
	if err != nil {
		return nil
	}
	return b
}

// textEntry emits a single content entry of the given type, or nothing when the
// content block carries no text (a content-less chunk is not surfaced — unlike a
// thinking marker, an empty ACP text chunk has nothing to show). meta is the
// containing update's `_meta`, forwarded via Raw when present.
func textEntry(t agent.SessionEntryType, block api.ContentBlock, meta map[string]any) []agent.ChatEvent {
	text := contentBlockText(block)
	if text == "" {
		return nil
	}
	return []agent.ChatEvent{{
		Entry: &agent.SessionEntry{Type: t, Content: text, ContentBlocks: []agent.ContentBlock{blockToIR(block)}},
		Raw:   metaRaw(meta),
	}}
}

// mapToolCall turns a new tool_call into a tool_use entry: the human-readable
// title (falling back to the tool kind) names the tool, and rawInput carries the
// arguments. The engine-native toolCallId, kind, and locations are carried
// through so a re-emission (internal/acpagent/mapping.go) can reuse the SAME id
// instead of generating one keyed only by tool name.
func mapToolCall(tc *api.SessionUpdateToolCall) []agent.ChatEvent {
	if tc == nil {
		return nil
	}
	name := tc.Title
	if name == "" && tc.Kind != "" {
		name = string(tc.Kind)
	}
	return []agent.ChatEvent{{
		Entry: &agent.SessionEntry{
			Type:          agent.EntryTypeToolUse,
			ToolName:      name,
			ToolInput:     rawJSON(tc.RawInput),
			ToolCallID:    string(tc.ToolCallId),
			ToolKind:      string(tc.Kind),
			ToolLocations: locationsToIR(tc.Locations),
		},
		Raw: metaRaw(tc.Meta),
	}}
}

// mapToolCallUpdate turns a tool_call_update into a tool_result entry once it has
// something to report — completed/failed status, or produced content/output.
// Bare in-progress ticks (no output) yield nothing: they're status noise, not a
// result. A failed status marks the entry as an error. toolCallId/kind/
// locations carry through as mapToolCall does, and Content is ALSO kept
// structurally (ToolContent) alongside the flattened ToolOutput string.
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
	// Structured content counts as something to report even when it flattens to
	// no text: terminal content carries only a terminal id (its output is fetched
	// over terminal/output — see toolContentText), and that id is exactly how a
	// frontend learns WHERE this tool's live output is going. Judging the frame
	// by the flattened string alone dropped it as status noise.
	if output == "" && len(tu.Content) == 0 && !terminal {
		return nil
	}
	title := ""
	if tu.Title != nil {
		title = *tu.Title
	}
	var kind string
	if tu.Kind != nil {
		kind = string(*tu.Kind)
	}
	return []agent.ChatEvent{{
		Entry: &agent.SessionEntry{
			Type:          agent.EntryTypeToolResult,
			ToolName:      title,
			ToolOutput:    output,
			IsError:       status == api.ToolCallStatusFailed,
			ToolCallID:    string(tu.ToolCallId),
			ToolKind:      kind,
			ToolLocations: locationsToIR(tu.Locations),
			ToolContent:   toolContentToIR(tu.Content),
		},
		Raw: metaRaw(tu.Meta),
	}}
}

// mapPlan carries the agent's execution plan through STRUCTURALLY: Plan
// holds the entries verbatim (content/priority/status) so a re-emission can
// rebuild a real ACP `plan` update, and SystemKind marks this as a plan entry
// (not the delegated-turn-failure notice, EntryTypeSystem's other producer —
// see agent.SessionSystemKind's doc comment). Content still carries the
// rendered checklist, unchanged, as a fallback for a consumer that only reads
// text (e.g. a transcript viewer, or a distillation pass).
func mapPlan(p *api.SessionUpdatePlan) []agent.ChatEvent {
	if p == nil || len(p.Entries) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("Plan:")
	entries := make([]agent.PlanEntry, 0, len(p.Entries))
	for _, pe := range p.Entries {
		entries = append(entries, agent.PlanEntry{
			Content:  pe.Content,
			Priority: string(pe.Priority),
			Status:   string(pe.Status),
		})
		if pe.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "\n- [%s] %s", pe.Status, pe.Content)
	}
	return []agent.ChatEvent{{
		Entry: &agent.SessionEntry{
			Type:       agent.EntryTypeSystem,
			Content:    b.String(),
			SystemKind: agent.SystemKindPlan,
			Plan:       entries,
		},
		Raw: metaRaw(p.Meta),
	}}
}

// --- accounting update variants ---

// usage_update is a real spec SessionUpdate variant (api.UsageUpdate) as of
// schema-v1.19.0 — confirmed to already match ctxloom's prior hand-rolled
// shape exactly, so it is now decoded straight into the real type
// instead of a bespoke usageUpdateWire. It is intercepted here, before the
// strict api.SessionUpdate union ever sees it, because it feeds turn
// ACCOUNTING rather than an emitted entry — protocol v1 itself carries no
// token/cost/context-window/timing fields anywhere else (PromptResponse is
// stopReason alone), so this is the ONLY usage data any ACP agent delivers
// today.
//
// ctxloom's own session-info extension used to also
// be intercepted here (a "session_info_update"-discriminated frame carrying
// model/contextWindow in `_meta.ctxloom_session_info`), extracted into
// s.infoModel/s.infoWindow for completeMeta to fold into TurnMeta. That
// extraction is GONE: the emitter side (internal/acpagent/mapping.go's
// sessionInfoUpdateWire) no longer puts Model or ContextWindow there at all
// — Model already rides CO1's SessionConfigOption ("model" category) and
// ContextWindow already rides usage_update's own `size` field, both
// unconditionally, so there is nothing left for this side to extract for
// either fact. A session_info_update frame (ctxloom's own PermissionMode/
// MCPServers residue, OR a genuinely foreign engine's own real title/
// timestamp update) now simply falls through to the strict
// api.SessionUpdate decode below and lands in mapSessionUpdate's default
// case — dropped as an unmodeled variant, exactly the same harmless no-op
// outcome session_info_update already had for anything it didn't recognize
// before this change (see TestChat_ForeignSessionInfoUpdateIgnored). This does NOT
// newly propagate PermissionMode/MCPServers into agent.ChatSessionInfo for a
// nested ctxloom-driving-ctxloom hop — that was ALREADY unread before this
// change ("the frame also carries permissionMode/mcpServers, not yet read
// here"), and stays that way: propagating it is out of scope here, which is
// about foreign-client rendering, not nested-delegation header propagation.
const usageUpdateVariant = "usage_update"

// updateDiscriminator reads a raw update's sessionUpdate type tag without
// decoding the full variant. Malformed JSON reads as "" (not ours to handle).
func updateDiscriminator(raw json.RawMessage) string {
	var d struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(raw, &d)
	return d.SessionUpdate
}

// --- permission relay ---

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
	p := &agent.PermissionRequest{
		ID:         id,
		ToolInput:  rawJSON(req.ToolCall.RawInput),
		Kind:       toolKindString(req.ToolCall.Kind),
		ToolCallID: string(req.ToolCall.ToolCallId),
	}
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

// blockToIR projects one ACP content block onto the IR's structured form: Kind/
// Text for a consumer that only needs to know what it is, Raw verbatim for
// anything richer (image/audio/resource — multimodal intake, a later slice).
// A marshal failure leaves Raw empty (never expected: block was itself just
// decoded from JSON).
func blockToIR(block api.ContentBlock) agent.ContentBlock {
	kind, text := "", ""
	switch {
	case block.Text != nil:
		kind, text = "text", block.Text.Text
	case block.Image != nil:
		kind = "image"
	case block.Audio != nil:
		kind = "audio"
	case block.ResourceLink != nil:
		kind = "resource_link"
	case block.Resource != nil:
		kind = "resource"
	}
	raw, _ := json.Marshal(block)
	return agent.ContentBlock{Kind: kind, Text: text, Raw: raw}
}

// locationsToIR projects ACP tool-call locations onto the IR form.
func locationsToIR(locs []api.ToolCallLocation) []agent.ToolLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]agent.ToolLocation, 0, len(locs))
	for _, l := range locs {
		line := 0
		if l.Line != nil {
			line = *l.Line
		}
		out = append(out, agent.ToolLocation{Path: l.Path, Line: line})
	}
	return out
}

// toolContentToIR projects a tool call's structured content collection (ACP
// ToolCallContent union) onto the IR form, alongside toolContentText's
// flattened string projection (both are populated; neither replaces the
// other).
func toolContentToIR(content []api.ToolCallContent) []agent.ToolContentBlock {
	if len(content) == 0 {
		return nil
	}
	out := make([]agent.ToolContentBlock, 0, len(content))
	for _, tcc := range content {
		raw, _ := json.Marshal(tcc)
		switch {
		case tcc.Content != nil:
			out = append(out, agent.ToolContentBlock{Kind: "content", Text: contentBlockText(tcc.Content.Content), Raw: raw})
		case tcc.Diff != nil:
			old := ""
			if tcc.Diff.OldText != nil {
				old = *tcc.Diff.OldText
			}
			out = append(out, agent.ToolContentBlock{
				Kind: "diff", DiffPath: tcc.Diff.Path, DiffOldText: old, DiffNewText: tcc.Diff.NewText, Raw: raw,
			})
		case tcc.Terminal != nil:
			out = append(out, agent.ToolContentBlock{Kind: "terminal", TerminalID: string(tcc.Terminal.TerminalId), Raw: raw})
		}
	}
	return out
}

// toolContentText flattens a tool call's content collection (each element a
// ToolCallContent union: content block / diff / terminal reference) into
// text. Content is now PROPERLY TYPED as []api.ToolCallContent (the pinned
// SDK left this []interface{}/interface{}, requiring a remarshal-through-JSON
// dance this function used to do for every element — see the old module's
// unions_generated.go and this file's prior revision). The union carries no
// discriminator field of its own — dispatch switches on which variant
// pointer is non-nil.
//
// TERMINAL content contributes NO text, deliberately: the ACP terminal variant
// carries only a terminal id, whose output is fetched over terminal/output
// rather than delivered inline, so there is nothing here to flatten and
// inventing a placeholder would fabricate tool output. Its identity survives
// structurally in toolContentToIR's "terminal" block (TerminalID), and
// mapToolCallUpdate treats the presence of content — not this string — as the
// signal that a frame is worth emitting.
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
