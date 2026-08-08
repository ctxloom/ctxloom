package kiro

import (
	"encoding/json"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// converter carries the streamed per-turn pass's state: the record func, and
// a count of real conversation entries recorded so
// Convert's checkFloor can tell drift from a genuinely empty conversation
// (this is why the type is not vacuous). Unlike codex's
// converter, kiro's needs NO pending-boundary buffer across turns — every
// historyTurn already carries its own complete request_metadata alongside
// its content (schema.go's historyTurn doc comment), so there is nothing to
// correlate across turns before a Complete event can be built.
type converter struct {
	record func(agent.ChatEvent) error

	// entries counts real conversation ChatEvents recorded by handleTurn —
	// NOT the per-turn Complete boundary, which is accounting metadata every
	// turn emits regardless of content. checkFloor uses this to tell "a
	// conversation with real turns but zero recognizable content" apart from
	// a genuinely turn-less conversation.
	entries int
}

// checkFloor is the shared driver's flush step for kiro: a conversation with
// real history turns that converted to zero canonical entries (every turn's
// user/assistant content union unrecognized) must not report success — the
// vendor format this build parses no longer matches the document, and a
// silently empty transcript would permanently block a later, better import.
// The per-turn Complete boundary deliberately does not count toward the floor;
// see converter.entries above. Mirrors claude/codex/antigravity's
// identically-motivated floor check.
func (c *converter) checkFloor(conversationID string, turns int) error {
	if turns > 0 && c.entries == 0 {
		return fmt.Errorf("kiro: conversation %s has %d history turn(s) but converted ZERO transcript entries — the vendor format this build parses no longer matches the document", conversationID, turns)
	}
	return nil
}

// handleTurn maps one historyTurn (raw JSON, from conversationDoc.History)
// to zero or more ChatEvents: the user side's content union, then the
// assistant side's, then exactly one Complete boundary for the turn's own
// accounting. A turn whose JSON does not even unmarshal into historyTurn's
// shape is skipped entirely — including its Complete — the same
// degrade-to-partial contract vendorreader.VendorAdapter's doc comment promises;
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
		c.entries++
	}
	for _, ev := range assistantContentEvents(t.Assistant) {
		if err := c.record(ev); err != nil {
			return err
		}
		c.entries++
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
		return vendorreader.TextEntry(agent.EntryTypeUser, u.Prompt.Prompt)
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
// true for any Status other than statusSuccess (schema.go), which carries the
// full rationale for that inversion.
func toolResultEvents(results []toolUseResult) []agent.ChatEvent {
	if len(results) == 0 {
		return nil
	}
	evs := make([]agent.ChatEvent, 0, len(results))
	for _, r := range results {
		// Join every content element's Text, dropping any that are empty —
		// same convention codex's joinContentText uses for the analogous
		// content-block case.
		texts := make([]string, len(r.Content))
		for i, c := range r.Content {
			texts[i] = c.Text
		}
		evs = append(evs, vendorreader.ToolResultEvent(r.ToolUseID, vendorreader.JoinNonEmpty(texts), r.Status != statusSuccess, nil))
	}
	return evs
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
		evs := vendorreader.TextEntry(agent.EntryTypeAssistant, a.ToolUse.Content)
		for _, tu := range a.ToolUse.ToolUses {
			// tu.Args is already a bare JSON object on every real tool_uses
			// element captured on this box — unlike codex's
			// function_call.arguments, a JSON-ENCODED STRING that itself
			// contains an object (codex/rollout.go's argumentsToRaw doc
			// comment) — so no string-unwrap step applies here; ToolUseEvent's
			// own nonEmptyRaw normalizes an empty/absent args to nil.
			evs = append(evs, vendorreader.ToolUseEvent(tu.Name, tu.ID, tu.Args))
		}
		return evs
	case a.Response != nil:
		return vendorreader.TextEntry(agent.EntryTypeAssistant, a.Response.Content)
	default:
		return nil // an unmodeled/future assistantContent variant: skip, not fatal
	}
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
	var b vendorreader.SessionInfoBuilder

	// Only the DOCUMENT's own conversation_id counts toward
	// SessionInfoBuilder's "found" contract here: the locator's
	// conversationID is Convert's own lookup key, known unconditionally for
	// every call, so latching it via SetSessionID would make found() true
	// even for a document that carried NO session metadata of its own,
	// defeating SessionInfoBuilder's documented "nil means nothing observed"
	// contract and (via the lazily-created Recorder) leaving a Session-only
	// canonical file behind for a conversation that was otherwise never
	// really captured — permanently blocking a later, better import.
	b.SetSessionID(doc.ConversationID)

	if doc.ModelInfo != nil {
		model := doc.ModelInfo.ModelID
		if model == "" {
			model = doc.ModelInfo.ModelName
		}
		b.SetModel(model)
		b.SetContextWindow(doc.ModelInfo.ContextWindowTokens)
	}

	// PermissionMode is left empty: no field anywhere in a real
	// conversations_v2 row (grepped for trust/approv/permission/mode across
	// every real row on this box) carries kiro-cli's equivalent of codex's
	// approval_policy — an honest gap, not an oversight.

	info := b.Build()
	if info == nil {
		return nil
	}
	// doc.ConversationID was byte-identical to the locator's own conversation
	// id on every real row on this box; this fallback fills the SAME value
	// in for the rare case the document's own field was empty — but only
	// once real session metadata (model/context window) is already known to
	// exist, never as the sole reason to manufacture a Session event.
	if info.SessionID == "" {
		info.SessionID = conversationID
	}
	return info
}
