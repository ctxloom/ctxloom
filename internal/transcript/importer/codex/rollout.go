package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
)

// rolloutLine is codex's outer envelope for every line of a
// rollout-*.jsonl file: {timestamp, type, payload}. type selects which shape
// payload actually has (session_meta | event_msg | response_item |
// world_state | turn_context); the conversational content this adapter
// cares about sits inside payload, never at this top level. This is the
// exact distinction the deleted reader got wrong (see codex.go's package
// doc) — decoding straight into a flat struct with type/role/content fields
// at the top compiles fine and silently produces zero values for every real
// field, because none of them are actually there.
type rolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// eventMsgHead decodes just enough of an event_msg envelope's payload to
// dispatch on its own nested type discriminator (task_started | user_message
// | agent_message | token_count | task_complete are the variants observed on
// this box; an unrecognized one is skipped, not an error).
type eventMsgHead struct {
	Type string `json:"type"`
}

// sessionMetaPayload is event type "session_meta". id is the session's
// native id on current codex builds; session_id is the same value on older
// builds that carried both keys (verified across rollout files on this box:
// newer captures have only "id", older ones have both, none have only
// "session_id") — scanSessionInfo falls back to it so the adapter doesn't
// silently lose the session id on an older codex CLI version.
type sessionMetaPayload struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}

// taskStartedPayload is event_msg payload type "task_started" — the only
// observed source of the model's context window.
type taskStartedPayload struct {
	ModelContextWindow int `json:"model_context_window"`
}

// turnContextPayload is rolloutLine type "turn_context" — carries the
// resolved model and approval policy for the turn that follows it.
// approval_policy is codex's own vocabulary (e.g. "on-request"), passed
// through to ChatSessionInfo.PermissionMode verbatim rather than translated
// to some shared enum: that field is a free-form per-engine string
// everywhere else it's populated too.
type turnContextPayload struct {
	Model          string `json:"model"`
	ApprovalPolicy string `json:"approval_policy"`
}

// responseItemPayload is rolloutLine type "response_item" — the superset of
// fields across its four sub-variants (message, reasoning, function_call,
// function_call_output), discriminated by its own nested Type.
type responseItemPayload struct {
	Type      string            `json:"type"`
	Role      string            `json:"role,omitempty"`
	Content   []contentBlock    `json:"content,omitempty"`
	Summary   []json.RawMessage `json:"summary,omitempty"`
	Name      string            `json:"name,omitempty"`      // function_call
	Arguments string            `json:"arguments,omitempty"` // function_call: a JSON-ENCODED STRING, not an object
	CallID    string            `json:"call_id,omitempty"`   // function_call / function_call_output
	Output    string            `json:"output,omitempty"`    // function_call_output
}

// contentBlock is one element of a message response_item's content array
// (type "input_text" for developer/user roles, "output_text" for assistant).
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// tokenCountPayload is event_msg payload type "token_count". Two counters
// live under info: total_token_usage is a CUMULATIVE running total across
// the whole session, last_token_usage is THIS turn's own consumption
// (confirmed by inspecting a real two-turn session on this box: input_tokens
// 28919 then 29010 in last_token_usage summed to exactly the second
// total_token_usage.input_tokens of 57929/28919+29010). agent.TurnMeta is
// per-RESPONSE accounting, so this adapter reads last_token_usage, not the
// cumulative total — using the cumulative figure would misreport every turn
// after the first as including every prior turn's tokens too.
type tokenCountPayload struct {
	Info struct {
		LastTokenUsage struct {
			InputTokens       int `json:"input_tokens"`
			CachedInputTokens int `json:"cached_input_tokens"`
			OutputTokens      int `json:"output_tokens"`
		} `json:"last_token_usage"`
		ModelContextWindow int `json:"model_context_window"`
	} `json:"info"`
}

// taskCompletePayload is event_msg payload type "task_complete" — the only
// observed source of a turn's wall-clock duration.
type taskCompletePayload struct {
	DurationMs int `json:"duration_ms"`
}

// convertLines runs the two-pass conversion via importer.ConvertJSONLLines:
// scanSessionInfo first (so a single Session ChatEvent, with every field it
// can find anywhere in the file, is recorded ONCE up front — matching
// agent.ChatEvent.Session's "emitted once near the start" contract even
// though codex spreads the contributing fields across three different
// envelope types at three different points in the file), then a streamed
// second pass in the file's own order for every entry/accounting event.
func convertLines(ctx context.Context, rec transcript.Recorder, lines [][]byte) error {
	c := &converter{record: importer.RecordFunc(rec, "codex")}
	err := importer.ConvertJSONLLines(ctx, rec, lines, "codex", scanSessionInfo(lines),
		func(line []byte) error {
			var env rolloutLine
			if jsonErr := json.Unmarshal(line, &env); jsonErr != nil {
				c.malformed++
				return nil // malformed line: skip, never fatal (see importer.VendorAdapter doc)
			}
			switch env.Type {
			case "response_item":
				return c.handleResponseItem(env.Payload)
			case "event_msg":
				return c.handleEventMsg(env.Payload)
			}
			// session_meta / turn_context / world_state contribute no entries
			// of their own here — session_meta and turn_context were already
			// folded into the single up-front Session event by
			// scanSessionInfo, and world_state carries codex's own
			// environment snapshot (AGENTS.md text, etc.), not conversation
			// content.
			return nil
		},
		c.flushPending)
	if err != nil {
		return err
	}
	return c.checkFloor(len(lines))
}

// checkFloor is codex's counterpart to claude's identically-shaped method
// (claude/session.go, U147-F01's fix): a rollout file that decodes cleanly
// and even contains response_item lines this adapter claims to understand,
// yet converts to zero canonical entries, must not report success — the
// vendor format drifted out from under this build. transcript.Recorder only
// creates its file on the first SUCCESSFUL Record (recorder.go's NewRecorder
// doc), so without this the same drifted file would silently "succeed"
// again on every future retry, forever (U148-F01).
func (c *converter) checkFloor(total int) error {
	if c.entries > 0 {
		return nil
	}
	switch {
	case c.conversational > 0:
		return fmt.Errorf("codex: read %d line(s) including %d response_item line(s) this adapter claims to understand, but converted ZERO transcript entries — the vendor format this build parses no longer matches the file", total, c.conversational)
	case c.malformed > 0 && c.malformed == total:
		return fmt.Errorf("codex: all %d line(s) failed to parse as JSON — not a rollout this build can read", total)
	}
	return nil
}

// scanSessionInfo makes one pass over every line looking for the three
// envelope types that contribute session-level metadata, latching each field
// onto its FIRST occurrence only via importer.SessionInfoBuilder (mirrors
// Recorder.Record's own "latch onto the first KindSession line" discipline
// in recorder.go). Returns nil if the file contained none of them at all
// (nothing to record).
func scanSessionInfo(lines [][]byte) *agent.ChatSessionInfo {
	var b importer.SessionInfoBuilder
	for _, line := range lines {
		var env rolloutLine
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		switch env.Type {
		case "session_meta":
			var p sessionMetaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				continue
			}
			id := p.ID
			if id == "" {
				id = p.SessionID
			}
			b.SetSessionID(id)
		case "event_msg":
			var head eventMsgHead
			if err := json.Unmarshal(env.Payload, &head); err != nil || head.Type != "task_started" {
				continue
			}
			var p taskStartedPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				continue
			}
			b.SetContextWindow(p.ModelContextWindow)
		case "turn_context":
			var p turnContextPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				continue
			}
			b.SetModel(p.Model)
			b.SetPermissionMode(p.ApprovalPolicy)
		}
	}
	return b.Build()
}

// converter carries the streamed second pass's only piece of cross-line
// state: a Complete record's fields arrive on TWO separate envelope types
// (token_count for accounting, task_complete for duration) with no shared id
// to correlate them by, so pending buffers whichever arrived first until the
// other completes the boundary and it is recorded as ONE ChatEvent.Complete
// — never two half-populated Complete lines masquerading as two separate
// turn boundaries.
//
// codex can emit MULTIPLE token_count events before the task_complete that
// closes one outer turn (observed: one after an intermediate tool-calling
// model round, one after the final-answer round) — each overwrites pending's
// token fields rather than accumulating, so only the LAST token_count before
// task_complete survives into the recorded Complete. This is deliberate, not
// a lossy oversight: last_token_usage is itself per-call, not cumulative
// (see tokenCountPayload's doc comment), and the task_complete boundary
// represents the OUTER turn's completion from the user's point of view — its
// paired accounting is honestly "however the final call inside that turn
// billed," not a sum across every internal round-trip codex made to get
// there.
type converter struct {
	record  func(agent.ChatEvent) error
	pending *agent.TurnMeta

	// Import accounting, read by checkFloor once the stream is done — mirrors
	// claude's identically-named fields (claude/session.go's converter doc
	// comment): how many response_item/event_msg lines this adapter claims to
	// understand (recognized envelope Type, whatever its sub-variant), how
	// many canonical entries actually came out, and how many lines failed to
	// parse as JSON at all.
	conversational int
	malformed      int
	entries        int
}

// flushPending records a still-open Complete boundary at end of file (a
// token_count with no matching task_complete — a truncated/interrupted
// rollout) rather than silently discarding accounting data that was
// genuinely captured. A nil pending is a normal, silent no-op: most files
// end cleanly on their own task_complete, which already flushed and cleared
// it — see importer.FlushComplete for the mechanics shared with claude's
// identical boundary-flush shape.
func (c *converter) flushPending() error {
	return importer.FlushComplete(&c.pending, c.record)
}

// handleResponseItem dispatches one response_item payload to its
// sub-variant's mapper and records every ChatEvent it yields (message: zero
// or one; function_call/function_call_output: exactly one; reasoning: zero
// or one).
func (c *converter) handleResponseItem(raw json.RawMessage) error {
	var p responseItemPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil // malformed: skip, never fatal
	}
	var evs []agent.ChatEvent
	switch p.Type {
	case "message":
		evs = messageEvents(p)
	case "reasoning":
		evs = reasoningEvents(p)
	case "function_call":
		evs = functionCallEvents(p)
	case "function_call_output":
		evs = functionCallOutputEvents(p)
	default:
		return nil // an unmodeled/future response_item variant: skip, not fatal
	}
	c.conversational++
	for _, ev := range evs {
		if err := c.record(ev); err != nil {
			return err
		}
		c.entries++
	}
	return nil
}

// handleEventMsg dispatches one event_msg payload; only token_count and
// task_complete contribute anything (see tokenCountPayload/
// taskCompletePayload doc comments) — user_message/agent_message are
// SKIPPED deliberately: they duplicate the same content the response_item
// message entries already carry (codex emits both a response_item AND an
// event_msg notification for the same user/assistant turn), and recording
// both would double every user and assistant entry in the canonical
// transcript.
func (c *converter) handleEventMsg(raw json.RawMessage) error {
	var head eventMsgHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil
	}
	switch head.Type {
	case "token_count":
		var p tokenCountPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil
		}
		if c.pending == nil {
			c.pending = &agent.TurnMeta{}
		}
		c.pending.InputTokens = p.Info.LastTokenUsage.InputTokens
		c.pending.OutputTokens = p.Info.LastTokenUsage.OutputTokens
		c.pending.CacheReadTokens = p.Info.LastTokenUsage.CachedInputTokens
		// Absent is not zero. The token fields above DO overwrite — each
		// last_token_usage is per-call, and only the last one before
		// task_complete belongs to the boundary (tokenCountPayload's doc
		// comment) — but the context window is a property of the model, not of
		// the call, and codex does not repeat it on every token_count. Writing
		// the decoded zero from an event that simply omitted it would erase a
		// window an earlier event already reported, leaving the Complete
		// record claiming a context window no codex build ever emits. A
		// genuinely new non-zero window still wins.
		if p.Info.ModelContextWindow != 0 {
			c.pending.ContextWindow = p.Info.ModelContextWindow
		}
		return nil
	case "task_complete":
		var p taskCompletePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil
		}
		if c.pending == nil {
			c.pending = &agent.TurnMeta{}
		}
		c.pending.DurationMs = p.DurationMs
		return c.flushPending()
	default:
		return nil
	}
}

// messageEvents maps a "message" response_item to zero or one entries.
// Only role user/assistant become canonical entries — role "developer" is
// codex's own system-prompt/instruction-injection channel (ctxloom's
// assembled project context, base_instructions, permission notices), not
// user-authored conversation; recording it as a "user" entry would
// misrepresent ctxloom's own context assembly as something the human typed.
// docs/transcript-schema.md §2c's mapping table names only response_item
// role=user/assistant as canonical turns, matching this choice.
func messageEvents(p responseItemPayload) []agent.ChatEvent {
	switch p.Role {
	case "user":
		return importer.TextEntry(agent.EntryTypeUser, joinContentText(p.Content))
	case "assistant":
		return importer.TextEntry(agent.EntryTypeAssistant, joinContentText(p.Content))
	}
	return nil
}

// joinContentText concatenates every content block's text (input_text for a
// developer/user message, output_text for assistant — this adapter doesn't
// gate on the exact type string, since the intent is simply "visible text in
// this message" and codex has not been observed emitting a non-text block in
// a message response_item on this box).
//
// Deliberately NOT delegating to importer.JoinNonEmpty (the join primitive
// joinSummaryText below and every other engine's adapter use): doing so
// makes this function's normalized shape match an unrelated,
// pre-existing near-duplicate outside this package — internal/operations/
// resume.go's JoinLeadBlocks, a resume-prompt helper with nothing to do with
// vendor-transcript import — which reprise's check flags as an inconsistent
// partial update the moment either side is touched. Fixing that properly
// means changing internal/operations, out of scope for a change confined to
// internal/transcript/importer/*.
func joinContentText(blocks []contentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// reasoningEvents maps a "reasoning" response_item to zero or one thinking
// entries. codex's reasoning.summary was EMPTY on every rollout file
// available on this box (its actual reasoning lives only in
// encrypted_content, opaque ciphertext ctxloom cannot decode) — so an empty
// summary is not an error, just honestly nothing visible to capture,
// matching this schema's documented fidelity gaps (docs/transcript-schema.md
// §4). summary's element shape was never observed non-empty here either;
// joinSummaryText defensively accepts both a bare string and a {"text":...}
// object per element, since codex's own source was not available to confirm
// which one a non-empty summary actually uses.
func reasoningEvents(p responseItemPayload) []agent.ChatEvent {
	return importer.TextEntry(agent.EntryTypeThinking, joinSummaryText(p.Summary))
}

func joinSummaryText(items []json.RawMessage) string {
	var parts []string
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			parts = append(parts, s)
			continue
		}
		var obj struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item, &obj); err == nil {
			parts = append(parts, obj.Text)
		}
	}
	return importer.JoinNonEmpty(parts)
}

// functionCallEvents maps a "function_call" response_item to exactly one
// tool_use entry.
func functionCallEvents(p responseItemPayload) []agent.ChatEvent {
	input := argumentsToRaw(p.Arguments)
	// U148-F02: ToolUseEvent (unlike TextEntry) always returns a populated
	// event, even when name/call_id/input all decoded empty — a fully
	// drifted function_call payload would otherwise still emit a hollow
	// tool_use entry, breaking the emptiness discipline TextEntry enforces
	// for the message/reasoning sibling paths. Nothing recognizable at all:
	// contribute nothing, same as an unmodeled response_item variant.
	if p.Name == "" && p.CallID == "" && len(input) == 0 {
		return nil
	}
	return []agent.ChatEvent{importer.ToolUseEvent(p.Name, p.CallID, input)}
}

// argumentsToRaw converts function_call's `arguments` field — documented and
// observed as a JSON-ENCODED STRING (e.g. `"{\"cmd\":\"...\"}"`), not a bare
// JSON object — into the json.RawMessage agent.SessionEntry.ToolInput
// expects. This is the second half of the envelope bug the deleted reader
// got wrong: even a reader that unwrapped the outer envelope correctly could
// still fail here by assuming arguments was already an object instead of a
// string that itself contains one.
func argumentsToRaw(args string) json.RawMessage {
	if args == "" {
		return nil
	}
	if json.Valid([]byte(args)) {
		return json.RawMessage(args)
	}
	// Defensive fallback for a hypothetical non-JSON arguments string: wrap it
	// as a JSON string literal so ToolInput is always valid JSON, never a raw
	// byte sequence that breaks a downstream json.Unmarshal. Not observed on
	// this box (every captured arguments value was valid JSON). json.Marshal
	// of a string cannot fail (invalid UTF-8 is replaced with U+FFFD, never
	// reported as an error), so there is no error branch to handle here.
	wrapped, _ := json.Marshal(args)
	return wrapped
}

// functionCallOutputEvents maps a "function_call_output" response_item to
// exactly one tool_result entry. IsError is always left false: codex's
// function_call_output envelope carries no explicit success/failure field on
// this build (verified — call_id/output/type are its only three keys across
// every captured rollout on this box); a consumer wanting pass/fail must
// parse Output's own free text itself (e.g. "Process exited with code N").
// This adapter does not fabricate a boolean from prose — an honest gap, not
// a silent guess.
func functionCallOutputEvents(p responseItemPayload) []agent.ChatEvent {
	// U148-F02: mirror functionCallEvents' emptiness discipline — a
	// call_id-less, output-less function_call_output is nothing recognizable,
	// not a hollow tool_result to emit anyway.
	if p.CallID == "" && p.Output == "" {
		return nil
	}
	return []agent.ChatEvent{importer.ToolResultEvent(p.CallID, p.Output, false)}
}
