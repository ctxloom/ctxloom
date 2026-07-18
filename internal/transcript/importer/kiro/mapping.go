package kiro

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// converter carries the streamed per-turn pass's only piece of state: the
// Recorder itself. Unlike codex's converter, kiro's needs NO pending-
// boundary buffer across turns — every historyTurn already carries its own
// complete request_metadata alongside its content (schema.go's historyTurn
// doc comment), so there is nothing to correlate across turns before a
// Complete event can be built.
type converter struct {
	rec transcript.Recorder
}

func (c *converter) record(ev agent.ChatEvent) error {
	if err := c.rec.Record(ev); err != nil {
		return fmt.Errorf("kiro: record: %w", err)
	}
	return nil
}

// handleTurn maps one historyTurn (raw JSON, from conversationDoc.History)
// to zero or more ChatEvents: the user side's content union, then the
// assistant side's, then exactly one Complete boundary for the turn's own
// accounting. A turn whose JSON does not even unmarshal into historyTurn's
// shape is skipped entirely — including its Complete — the same
// degrade-to-partial contract importer.VendorAdapter's doc comment promises;
// there is no accounting to honestly report for a turn that could not be
// parsed at all.
func (c *converter) handleTurn(raw json.RawMessage, modelInfo *modelInfo) error {
	var t historyTurn
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil // malformed turn: skip, never fatal
	}
	for _, ev := range userContentEvents(t.User.Content) {
		if err := c.record(ev); err != nil {
			return err
		}
	}
	for _, ev := range assistantContentEvents(t.Assistant) {
		if err := c.record(ev); err != nil {
			return err
		}
	}
	return c.record(agent.ChatEvent{Complete: turnMeta(t.RequestMetadata, modelInfo)})
}

// userContentEvents maps a turnUser.Content union (schema.go's userContent
// doc comment) to zero or more entries. For a CancelledToolUses turn, order
// is deliberate: kiro-cli's own rejection notice FIRST (it explains what
// follows), then one tool_result entry per tool_use_results element —
// mirroring a reader's natural "why, then what happened" order.
func userContentEvents(raw json.RawMessage) []agent.ChatEvent {
	var u userContent
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil // malformed/unrecognized content union: skip, not fatal
	}
	switch {
	case u.Prompt != nil:
		if u.Prompt.Prompt == "" {
			return nil
		}
		return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: u.Prompt.Prompt}}}
	case u.ToolUseResults != nil:
		return toolResultEvents(u.ToolUseResults.ToolUseResults)
	case u.CancelledToolUses != nil:
		var evs []agent.ChatEvent
		if u.CancelledToolUses.Prompt != "" {
			evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:       agent.EntryTypeSystem,
				SystemKind: agent.SystemKindNotice,
				Content:    u.CancelledToolUses.Prompt,
			}})
		}
		evs = append(evs, toolResultEvents(u.CancelledToolUses.ToolUseResults)...)
		return evs
	default:
		return nil // an unmodeled/future userContent variant: skip, not fatal
	}
}

// toolResultEvents maps a tool_use_results array (shared by ToolUseResults
// and CancelledToolUses) to one tool_result entry per element. IsError is
// true for any Status other than the literal "Success" — see
// toolUseResult's doc comment for why this is reading the vendor's own
// signal, not guessing one the way codex must for function_call_output.
func toolResultEvents(results []toolUseResult) []agent.ChatEvent {
	if len(results) == 0 {
		return nil
	}
	evs := make([]agent.ChatEvent, 0, len(results))
	for _, r := range results {
		evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:       agent.EntryTypeToolResult,
			ToolCallID: r.ToolUseID,
			ToolOutput: joinToolResultText(r.Content),
			IsError:    r.Status != "Success",
		}})
	}
	return evs
}

// joinToolResultText concatenates every content element's non-empty Text
// directly into a builder (rather than collecting a []string to hand to
// strings.Join, codex's joinContentText's approach for the analogous
// message-content-block case) — a real tool_use_results array is short
// (one element on every real capture on this box), so this avoids the
// intermediate slice allocation strings.Join's approach needs for no
// behavioral difference.
func joinToolResultText(items []toolResultContent) string {
	var b strings.Builder
	for _, it := range items {
		if it.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(it.Text)
	}
	return b.String()
}

// assistantContentEvents maps an assistantContent union to zero or more
// entries. ToolUse.Content (lead-in prose alongside the tool call), when
// present, is emitted FIRST, ahead of the tool_use entries that follow it in
// the same turn — see toolUseAssistant's doc comment for why this was never
// observed non-empty on this box, and is still mapped defensively.
func assistantContentEvents(raw json.RawMessage) []agent.ChatEvent {
	var a assistantContent
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil // malformed/unrecognized content union: skip, not fatal
	}
	switch {
	case a.ToolUse != nil:
		var evs []agent.ChatEvent
		if a.ToolUse.Content != "" {
			evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: a.ToolUse.Content}})
		}
		for _, tu := range a.ToolUse.ToolUses {
			evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:       agent.EntryTypeToolUse,
				ToolName:   tu.Name,
				ToolCallID: tu.ID,
				ToolInput:  toolArgsToRaw(tu.Args),
			}})
		}
		return evs
	case a.Response != nil:
		if a.Response.Content == "" {
			return nil
		}
		return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: a.Response.Content}}}
	default:
		return nil // an unmodeled/future assistantContent variant: skip, not fatal
	}
}

// toolArgsToRaw passes a tool_uses element's args straight through as
// SessionEntry.ToolInput. Unlike codex's function_call.arguments (a
// JSON-ENCODED STRING that itself contains an object — codex/rollout.go's
// argumentsToRaw doc comment), kiro's args was ALREADY a bare JSON object on
// every real tool_uses element captured on this box, so no string-unwrap
// step applies here; an empty/absent args (json.RawMessage(nil)) passes
// through as nil, matching argumentsToRaw's own "empty means nil" contract
// for ToolInput.
func toolArgsToRaw(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return nil
	}
	return args
}

// turnMeta builds one Complete boundary from a turn's own request_metadata,
// plus mi's session-level context window (mi may be nil — an absent
// model_info leaves ContextWindow at its zero/"unknown" value, same as
// every other field here). See requestMetadata's doc comment for which
// fields were genuinely populated (timestamps) vs honestly absent (every
// token counter) on every real conversation inspected on this box.
// ContextWindow is repeated on EVERY turn's Complete (not just the
// session's own up-front Session event) deliberately — mirrors codex's
// tokenCountPayload.ModelContextWindow, which does the same per-Complete
// repetition rather than relying on a consumer to remember the session-level
// value from earlier in the stream.
func turnMeta(rm requestMetadata, mi *modelInfo) *agent.TurnMeta {
	tm := &agent.TurnMeta{
		Model:               rm.ModelID,
		InputTokens:         nz(rm.UncachedInputTokens),
		OutputTokens:        nz(rm.OutputTokens),
		CacheReadTokens:     nz(rm.CacheReadInputTokens),
		CacheCreationTokens: nz(rm.CacheWriteInputTokens),
		DurationMs:          durationMs(rm),
	}
	if mi != nil {
		tm.ContextWindow = mi.ContextWindowTokens
	}
	return tm
}

func nz(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// durationMs computes a turn's wall-clock duration from its own
// request_start/stream_end timestamps. Returns 0 (TurnMeta's "unknown"
// convention) for any pairing that isn't a genuine positive interval —
// either timestamp missing/zero, or a stream_end that does not follow
// request_start — rather than emit a nonsensical negative or fabricated
// duration.
func durationMs(rm requestMetadata) int {
	if rm.RequestStartTimestampMs <= 0 || rm.StreamEndTimestampMs <= 0 || rm.StreamEndTimestampMs < rm.RequestStartTimestampMs {
		return 0
	}
	return int(rm.StreamEndTimestampMs - rm.RequestStartTimestampMs)
}

// sessionInfo builds the single up-front Session ChatEvent from doc's
// session-level fields (conversationID, model_info) — kiro needs no
// codex-style scan-then-latch across multiple envelope occurrences, because
// the whole document is ALREADY decoded in one shot by the time Convert
// calls this: there is only ever one candidate value for each field, not
// several arriving at different points in a stream.
func sessionInfo(conversationID string, doc *conversationDoc) *agent.ChatSessionInfo {
	info := &agent.ChatSessionInfo{}
	found := false

	// doc.ConversationID was byte-identical to the locator's own
	// conversation id on every real row on this box; the fallback exists so
	// a document that is missing (or has an empty) conversation_id of its
	// own still gets a SessionID from the one value Convert already knows
	// for certain — the id it looked the row up by.
	sid := doc.ConversationID
	if sid == "" {
		sid = conversationID
	}
	if sid != "" {
		info.SessionID = sid
		found = true
	}

	if doc.ModelInfo != nil {
		model := doc.ModelInfo.ModelID
		if model == "" {
			model = doc.ModelInfo.ModelName
		}
		if model != "" {
			info.Model = model
			found = true
		}
		if doc.ModelInfo.ContextWindowTokens != 0 {
			info.ContextWindow = doc.ModelInfo.ContextWindowTokens
			found = true
		}
	}

	// PermissionMode is left empty: no field anywhere in a real
	// conversations_v2 row (grepped for trust/approv/permission/mode across
	// every real row on this box) carries kiro-cli's equivalent of codex's
	// approval_policy — an honest gap, not an oversight.

	if !found {
		return nil
	}
	return info
}
