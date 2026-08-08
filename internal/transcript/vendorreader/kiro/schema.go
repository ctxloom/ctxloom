package kiro

import "encoding/json"

// conversationDoc is the top-level shape of conversations_v2.value —
// kiro-cli's OWN Rust-serialized conversation state. It is reverse-engineered
// from a REAL data.sqlite3 on this box
// (~/.local/share/kiro-cli/data.sqlite3 — NOT under ~/.kiro/, which is a
// different directory kiro-cli also uses for skills/steering/settings but
// never for its conversation store; docs/transcript-schema.md §2b's mention
// of a "v2 sqlite data.sqlite3/conversations_v2" gives no exact path, and the
// deleted v1 reader (internal/kiro/session.go, per that same doc) looked at
// ~/.kiro/sessions/cli/<id>.jsonl instead — a file this box's real kiro-cli
// installs do not even write). Every field below was inspected directly out
// of that real file (read-only, never mutated) via Python's stdlib sqlite3 +
// json before writing any Go — see testdata/MANIFEST.json for exactly which
// fixture fields are copied from it verbatim vs trimmed.
//
// Only the fields this adapter actually reads are modeled; every other key
// observed in a real row (tools, context_manager's tool-schema/mcp innards,
// file_line_tracker, user_turn_metadata, transcript — a redundant flattened
// text log the adapter does not need since history already carries
// everything structurally) decodes into nothing and is silently dropped by
// encoding/json, exactly as an unadorned struct field selection always does.
type conversationDoc struct {
	// ConversationID mirrors the conversations_v2.conversation_id column
	// (confirmed byte-identical on every real row on this box — see
	// kiro.go's sessionInfo doc comment for why Convert still falls back to
	// the column value from its locator when this is empty).
	ConversationID string `json:"conversation_id"`
	// History is kept as raw per-turn JSON, not decoded eagerly into
	// []historyTurn here, so ONE malformed turn cannot fail the whole
	// document's unmarshal — the same degrade-to-partial contract codex's
	// per-line JSONL decode gets for free from being line-oriented; kiro's
	// single JSON blob needs this explicit RawMessage split to get the same
	// property (see mapping.go's handleTurn).
	History   []json.RawMessage `json:"history"`
	ModelInfo *modelInfo        `json:"model_info"`
}

// modelInfo is conversationDoc.model_info — session-level (not per-turn)
// resolved-model metadata. ModelID was observed resolving to a real model
// name ("claude-sonnet-4.5", "qwen3-coder-next", etc.) on most real
// conversations on this box, and to the literal string "auto" (kiro-cli's
// own "let the service choose" alias) on the rest — both are copied through
// verbatim, never translated, matching every other adapter's "pass the
// vendor's own string through" discipline (e.g. codex's approval_policy).
type modelInfo struct {
	ModelName           string `json:"model_name"`
	ModelID             string `json:"model_id"`
	ContextWindowTokens int    `json:"context_window_tokens"`
}

// historyTurn is one element of conversationDoc.History: kiro-cli's own
// grouping of one user-side contribution, the assistant's response to it,
// and that single exchange's own accounting — all THREE co-located on one
// JSON object. This is the one structural way kiro's schema is SIMPLER than
// codex's: codex spreads a turn's content and its accounting across
// separate envelope lines arriving at different points in the file (a
// token_count event, then later a task_complete event, with no shared id to
// correlate them by — see codex/rollout.go's converter doc comment for the
// pending-boundary merge that requires). kiro's request_metadata is already
// the complete, final accounting for the SAME turn it rides alongside, so
// mapping.go's handleTurn needs no equivalent merge step at all.
type historyTurn struct {
	User      turnUser        `json:"user"`
	Assistant json.RawMessage `json:"assistant"`
	// RequestMetadata is decoded eagerly (not RawMessage): unlike
	// User.Content/Assistant, its shape is a single flat struct with no
	// vendor-side union tag to dispatch on, so there is nothing degrade-able
	// to defer here — a malformed request_metadata simply zero-values,
	// which turnMeta already treats as "unknown", its normal empty-field
	// convention.
	RequestMetadata requestMetadata `json:"request_metadata"`
}

// turnUser is historyTurn.user. additional_context/env_context/timestamp/
// images are not modeled: additional_context and env_context.env_state were
// EMPTY/redundant on every real turn on this box (env_context only ever
// restates the session's own already-known cwd), timestamp is present on a
// Prompt turn but null on a ToolUseResults turn (kiro-cli itself does not
// consistently stamp every turn), and images was null throughout — none of
// the three would improve on the envelope's own receipt-time TS or add
// content this adapter can act on. An honest, deliberate omission, not an
// oversight: unlike codex's zero-signal function_call_output.IsError gap,
// none of these three were ever observed non-empty to omit incorrectly.
type turnUser struct {
	// Content is kiro-cli's own externally-tagged Rust enum, kept as
	// RawMessage for the same "don't let one bad shape kill more than
	// itself" reason History does — see userContent's doc comment for the
	// decoded shape and mapping.go's userContentEvents for the dispatch.
	Content json.RawMessage `json:"content"`
}

// userContent is kiro-cli's own externally-tagged Rust enum
// (`#[serde(rename_all = ...)]`-free, i.e. Rust's DEFAULT "externally
// tagged" representation: the variant's own name IS the JSON object's only
// key, not a separate "type" discriminator field the way codex's envelope
// works — {"Prompt": {...}} rather than {"type": "prompt", ...}). Decoding
// straight into a struct with one pointer field per variant name works
// because encoding/json leaves every field whose key is absent as its zero
// value (nil, for a pointer) — no manual "peek at the key first" step
// needed, unlike codex's eventMsgHead/rolloutLine dispatch.
//
// Three variants were observed on this box: Prompt (a human-typed turn),
// ToolUseResults (a successful OR errored tool result being fed back —
// Status distinguishes them, see toolUseResult's doc comment), and
// CancelledToolUses (a tool call kiro-cli's OWN policy layer refused to run
// at all — see cancelledToolUsesContent's doc comment). All three, plus an
// unrecognized/future fourth, decode fine into this struct; an unrecognized
// one simply leaves every pointer nil, which userContentEvents treats as
// "nothing to record" — the same degrade discipline as an unmodeled
// response_item type in codex.
type userContent struct {
	Prompt            *promptContent            `json:"Prompt"`
	ToolUseResults    *toolUseResultsContent    `json:"ToolUseResults"`
	CancelledToolUses *cancelledToolUsesContent `json:"CancelledToolUses"`
}

type promptContent struct {
	Prompt string `json:"prompt"`
}

type toolUseResultsContent struct {
	ToolUseResults []toolUseResult `json:"tool_use_results"`
}

// cancelledToolUsesContent is kiro-cli's own record of a tool call its
// permission/policy layer refused to execute at all — observed verbatim on
// this box: Prompt == "Tool use with fs_write was rejected because the
// arguments supplied were forbidden. Blocked patterns:\n  - non-interactive
// mode (no user to approve)", with a single ToolUseResults entry carrying
// Status "Error" and content Text "Tool use was cancelled by the user" for
// the SAME tool_use_id the preceding assistant turn's ToolUse issued. Prompt
// here is kiro-cli's OWN generated explanation fed back to the model as if
// it were the next turn's user content — NOT anything a human typed — so
// mapping.go's userContentEvents maps it to an EntryTypeSystem notice, never
// EntryTypeUser, for the identical reason codex's messageEvents refuses to
// map its developer role to "user" (codex's rollout.go doc comment: "would
// misrepresent ctxloom's own context assembly as something the human
// typed" — same principle, kiro's own policy engine in place of ctxloom's
// context assembly).
type cancelledToolUsesContent struct {
	Prompt         string          `json:"prompt"`
	ToolUseResults []toolUseResult `json:"tool_use_results"`
}

// statusSuccess is the ONE toolUseResult.Status value that is not an error.
// It is kiro-cli's own literal, matched exactly and case-sensitively: this is
// a vendor string ctxloom does not control, so normalizing it (folding case,
// trimming) would be inventing a tolerance the vendor never promised, and
// would silently reclassify a future status that merely looks similar.
//
// Any OTHER value maps to IsError — the inverse default from codex's
// function_call_output, which carries no status field at all and so must
// default IsError false rather than guess. kiro's status field IS present, so
// treating "not Success" as an error reads the vendor's own signal rather than
// fabricating one. "Success" and "Error" are the only two values observed on
// this box.
const statusSuccess = "Success"

// toolUseResult is one element of a ToolUseResults/CancelledToolUses
// tool_use_results array. See statusSuccess for how Status is interpreted.
type toolUseResult struct {
	ToolUseID string              `json:"tool_use_id"`
	Content   []toolResultContent `json:"content"`
	Status    string              `json:"status"`
}

// toolResultContent is one element of a toolUseResult's content array. Every
// element observed on this box had exactly one key, "Text" — kiro-cli's own
// externally-tagged enum again, with only the text variant ever captured
// (no image/resource variant seen); an element with neither key present
// simply contributes no text, never an error.
type toolResultContent struct {
	Text string `json:"Text"`
}

// assistantContent mirrors userContent's externally-tagged shape for the
// assistant side: ToolUse (the model chose to call one or more tools) or
// Response (a plain final-answer turn). Decoded the same RawMessage-first
// way as User.Content (historyTurn.Assistant), for the same reason.
type assistantContent struct {
	ToolUse  *toolUseAssistant  `json:"ToolUse"`
	Response *responseAssistant `json:"Response"`
}

// toolUseAssistant is assistantContent's ToolUse variant. Content (lead-in
// prose alongside the tool call) was EMPTY on every real capture on this
// box — kiro-cli's own model apparently never streams prose alongside a
// tool call the way an ACP agent_message_chunk can precede a tool_call. An
// honest, unverified-for-non-empty gap, the same shape as codex's
// reasoning.summary (rollout.go's reasoningEvents doc comment) — still
// mapped defensively in mapping.go on the chance a future capture has one.
type toolUseAssistant struct {
	Content  string    `json:"content"`
	ToolUses []toolUse `json:"tool_uses"`
}

// toolUse is one element of toolUseAssistant.tool_uses. Args is kept as
// RawMessage (not decoded/re-marshaled): it is ALREADY a bare JSON object on
// every real capture on this box (unlike codex's function_call.arguments,
// which is a JSON-encoded STRING that itself contains an object — see
// codex/rollout.go's argumentsToRaw doc comment for that distinction) so no
// string-unwrap step is needed here at all — a second confirmed schema
// difference between the two vendors' otherwise-similar tool-call shapes.
// orig_name/orig_args (present on every real tool_uses element, always
// identical to name/args on this box — kiro-cli's own before/after-rewrite
// pair for a tool call its policy layer may have edited) are not modeled:
// this adapter has no rewrite history to reconcile and only ever needs the
// EFFECTIVE call kiro-cli actually issued.
type toolUse struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type responseAssistant struct {
	Content string `json:"content"`
}

// requestMetadata is one turn's own accounting, co-located with the turn
// (see historyTurn's doc comment). UncachedInputTokens/OutputTokens/
// CacheReadInputTokens/CacheWriteInputTokens/TotalTokens are Rust
// Option<u64> fields — JSON null or a number — and were observed NULL on
// EVERY turn of EVERY real conversation captured on this box: kiro-cli's
// local/non-billed captures apparently never populate them (whether that is
// true of every kiro-cli deployment, or specific to this box's account/plan,
// is not something inspecting one box's file can settle — flagged as an
// open question in the report, not silently assumed universal). They are
// still modeled as *int (not int) so turnMeta's nz() can honestly report
// "unknown" (TurnMeta's own zero-value convention, per agent/chat.go's doc
// comment) rather than a fabricated zero USAGE. RequestStartTimestampMs/
// StreamEndTimestampMs, by contrast, WERE populated on every real turn,
// giving DurationMs genuine (not gap-filled) fidelity even though token
// accounting is honestly absent.
type requestMetadata struct {
	ModelID                 string `json:"model_id"`
	RequestStartTimestampMs int64  `json:"request_start_timestamp_ms"`
	StreamEndTimestampMs    int64  `json:"stream_end_timestamp_ms"`
	UncachedInputTokens     *int   `json:"uncached_input_tokens"`
	OutputTokens            *int   `json:"output_tokens"`
	CacheReadInputTokens    *int   `json:"cache_read_input_tokens"`
	CacheWriteInputTokens   *int   `json:"cache_write_input_tokens"`
}
