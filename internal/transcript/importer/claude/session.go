package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// line is one top-level record of a claude transcript file. Unlike codex's
// rolloutLine, there is no envelope to unwrap — Type IS the discriminator,
// and every field this adapter cares about (aside from message content) sits
// at this same top level. Only "user" and "assistant" carry conversational
// content; every other observed Type (progress, queue-operation, system,
// attachment, last-prompt, mode, permission-mode, ai-title, custom-title,
// file-history-snapshot, agent-name, pr-link, worktree-state,
// file-history-delta, agent-color — confirmed by sampling real transcripts
// on this box) is administrative session/UI bookkeeping with no turn content
// of its own, and is silently skipped by convertLines' type switch exactly
// like codex skips session_meta/turn_context/world_state.
type line struct {
	Type string `json:"type"`
	// SessionID is claude's own session id, repeated verbatim on every line
	// of the file (including non-conversational ones) — unlike codex, where
	// only session_meta carries it, so the FIRST line of any type already
	// has it.
	SessionID string `json:"sessionId"`
	// IsSidechain marks a line belonging to claude's own in-harness subagent
	// (a Task-tool child) rather than the session's main thread — maps
	// directly onto agent.SessionEntry.Sidechain, a richer signal than codex
	// exposes at all.
	IsSidechain bool `json:"isSidechain"`
	// IsMeta marks a "user"-type line as claude-injected rather than
	// human-typed (e.g. the <local-command-caveat> wrapper claude prepends
	// around local-command output, or a bundled-skill body claude splices in
	// as a synthetic user turn) — confirmed by sampling: every isMeta:true
	// user line observed on this box carries exactly this kind of
	// claude-synthesized text, never something the human actually typed. The
	// same "don't misrepresent the harness's own injected content as
	// something the human typed" reasoning as codex's developer-role skip
	// (rollout.go's messageEvents doc comment) — see messageEntries below.
	IsMeta bool `json:"isMeta"`
	// PermissionMode rides on a genuinely human-typed "user" line only (never
	// observed on a tool_result-carrying or isMeta "user" line) — the
	// session's permission mode at the time that turn was sent.
	PermissionMode string `json:"permissionMode"`
	Message        *message `json:"message"`
}

// message is a line's "message" object. Content is left as json.RawMessage
// because claude represents it as EITHER a bare JSON string (a simple
// single-block text turn) OR an array of content-block objects (multi-block:
// thinking + tool_use, a tool_result-only turn, multiple text parts) —
// confirmed by sampling both shapes across real transcripts on this box.
// decodeContentBlocks below normalizes both into one []contentBlock slice so
// every caller handles a single shape.
type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model,omitempty"`
	ID         string          `json:"id,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *usage          `json:"usage,omitempty"`
}

// usage is the real Anthropic Messages API usage block claude's own
// assistant lines carry verbatim — unlike codex, whose accounting arrives on
// a SEPARATE event_msg.token_count envelope with no shared id to correlate
// it to a response_item by, claude's usage rides directly on the SAME
// message object as the content it accounts for (docs/transcript-schema.md
// §2c: "turn accounting | ... | `usage` on the assistant message | ...").
type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// contentBlock is one element of a normalized content-block list — either a
// real array element, or the single synthetic block decodeContentBlocks
// wraps a bare string in. Fields are a superset across every observed block
// type (text, thinking, tool_use, tool_result); which ones are populated
// depends on Type.
type contentBlock struct {
	Type string `json:"type"`
	// Text is populated for Type "text" (both a top-level message content
	// block and a tool_result's own nested content block, per
	// decodeContentBlocks).
	Text string `json:"text,omitempty"`
	// Thinking is populated for Type "thinking" — claude's real field name
	// for extended-thinking prose (NOT "text"; confirmed against real
	// captured thinking blocks on this box, which carry {type, thinking,
	// signature}, never a "text" key).
	Thinking string `json:"thinking,omitempty"`
	// ID/Name/Input are populated for Type "tool_use". Input is already a
	// JSON OBJECT here (unlike codex's function_call.arguments, which is a
	// JSON-ENCODED STRING that itself needs a second unmarshal) — confirmed
	// against real tool_use blocks on this box — so it passes straight
	// through to agent.SessionEntry.ToolInput with no re-encoding step.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// ToolUseID/IsError/Content are populated for Type "tool_result".
	// Content is itself dual-shaped exactly like message.Content (a bare
	// string for a simple textual result, or an array of blocks — observed
	// element types on this box: text, tool_reference, image) — decoded via
	// the SAME decodeContentBlocks helper, one level deeper.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// decodeContentBlocks normalizes claude's dual-shaped content field (bare
// JSON string | array of block objects — see message.Content and
// contentBlock.Content's doc comments) into one slice. A bare string becomes
// a single synthetic {Type: "text", Text: <the string>} block, so every
// caller — the top-level message content AND a tool_result's own nested
// content — handles exactly one shape. Returns nil for empty/malformed raw
// rather than erroring: an unparseable content field degrades to "no
// entries from this line," never aborts the whole conversion (importer.
// VendorAdapter's degrade-to-partial contract).
func decodeContentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []contentBlock{{Type: "text", Text: s}}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// convertLines runs the two-pass conversion, mirroring codex's convertLines:
// scanSessionInfo first (one Session ChatEvent recorded once up front), then
// a streamed second pass in the file's own order for every user/assistant
// line.
// The two-pass shape (scan once, then stream) is the reference pattern every
// importer.VendorAdapter copies from codex on purpose (codex.go's package
// doc); claude's payload/dispatch differs enough (no envelope, dual-shaped
// content, id-keyed accounting vs a second event type) that factoring a
// shared helper would abstract over the ONE thing that's actually different
// between vendors, not the boilerplate around it.
// reprise:ignore — intentional structural mirror of codex's convertLines, per the above.
func convertLines(ctx context.Context, rec transcript.Recorder, lines [][]byte) error {
	if info := scanSessionInfo(lines); info != nil {
		if err := rec.Record(agent.ChatEvent{Session: info}); err != nil {
			return fmt.Errorf("claude: record session: %w", err)
		}
	}

	c := &converter{rec: rec}
	for _, raw := range lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			continue // malformed line: skip, never fatal (see importer.VendorAdapter doc)
		}
		switch l.Type {
		case "user":
			if err := c.handleUser(l); err != nil {
				return err
			}
		case "assistant":
			if err := c.handleAssistant(l); err != nil {
				return err
			}
		}
		// Every other Type (progress, queue-operation, system, attachment,
		// last-prompt, mode, permission-mode, ai-title, custom-title,
		// file-history-snapshot, agent-name, pr-link, worktree-state,
		// file-history-delta, agent-color) contributes no entries: see
		// line's doc comment.
	}
	return c.flushPending()
}

// scanSessionInfo makes one pass over every line looking for session-level
// metadata, latching each field onto its FIRST occurrence only — mirrors
// codex's scanSessionInfo (rollout.go). Returns nil if the file contained
// none of it at all.
func scanSessionInfo(lines [][]byte) *agent.ChatSessionInfo {
	info := &agent.ChatSessionInfo{}
	found := false
	for _, raw := range lines {
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			continue
		}
		if l.SessionID != "" && info.SessionID == "" {
			info.SessionID = l.SessionID
			found = true
		}
		if l.Type == "assistant" && l.Message != nil {
			if l.Message.Model != "" && info.Model == "" {
				info.Model = l.Message.Model
				found = true
			}
		}
		if l.Type == "user" && l.PermissionMode != "" && info.PermissionMode == "" {
			info.PermissionMode = l.PermissionMode
			found = true
		}
	}
	if !found {
		return nil
	}
	return info
}

// converter carries the streamed second pass's cross-line state: claude's
// per-turn accounting (usage/model/stop_reason) rides on EVERY assistant
// line belonging to one model response (all sharing the same message.id —
// confirmed on this box: a response that streams multiple content blocks as
// separate JSONL lines repeats the identical usage/id/stop_reason on each
// one), so pending tracks the CURRENT response's accounting and pendingID
// tracks which message.id it belongs to. A Complete boundary is recorded
// when an assistant line's message.id DIFFERS from pendingID (a new model
// response has begun — the previous one is done) or at end of file
// (flushPending), never once per line — recording one per line would emit a
// separate Complete for every content block of the SAME response instead of
// one per actual turn.
type converter struct {
	rec       transcript.Recorder
	pending   *agent.TurnMeta
	pendingID string
}

func (c *converter) record(ev agent.ChatEvent) error {
	if err := c.rec.Record(ev); err != nil {
		return fmt.Errorf("claude: record: %w", err)
	}
	return nil
}

// flushPending records a still-open Complete boundary at end of file — a
// response whose accounting was captured but whose message.id never changed
// again before the file ended (a truncated/interrupted transcript, e.g. the
// user cancelled mid-turn). Mirrors codex's flushPending exactly. A nil
// pending is a normal, silent no-op.
func (c *converter) flushPending() error {
	if c.pending == nil {
		return nil
	}
	pending := c.pending
	c.pending = nil
	c.pendingID = ""
	return c.record(agent.ChatEvent{Complete: pending})
}

// handleUser dispatches one "user"-type line's content blocks to canonical
// entries.
func (c *converter) handleUser(l line) error {
	if l.Message == nil {
		return nil
	}
	blocks := decodeContentBlocks(l.Message.Content)
	evs := messageEntries("user", l.IsMeta, blocks)
	return c.recordAll(evs, l.IsSidechain)
}

// handleAssistant dispatches one "assistant"-type line's content blocks to
// canonical entries, then updates (and, on a message.id change, flushes) the
// pending turn-accounting boundary. Entries are recorded BEFORE the boundary
// bookkeeping runs, so a flushed Complete for the PREVIOUS response always
// lands after that response's own entries and before the new response's
// entries — the same ordering codex's task_complete handling produces.
func (c *converter) handleAssistant(l line) error {
	if l.Message == nil {
		return nil
	}
	blocks := decodeContentBlocks(l.Message.Content)
	evs := messageEntries("assistant", false, blocks)
	if err := c.recordAll(evs, l.IsSidechain); err != nil {
		return err
	}

	if l.Message.Usage == nil {
		return nil
	}
	if l.Message.ID != "" && c.pendingID != "" && l.Message.ID != c.pendingID {
		if err := c.flushPending(); err != nil {
			return err
		}
	}
	c.pendingID = l.Message.ID
	c.pending = &agent.TurnMeta{
		InputTokens:         l.Message.Usage.InputTokens,
		OutputTokens:        l.Message.Usage.OutputTokens,
		CacheReadTokens:     l.Message.Usage.CacheReadInputTokens,
		CacheCreationTokens: l.Message.Usage.CacheCreationInputTokens,
		Model:               l.Message.Model,
		StopReason:          l.Message.StopReason,
	}
	return nil
}

func (c *converter) recordAll(evs []agent.ChatEvent, sidechain bool) error {
	for _, ev := range evs {
		if ev.Entry != nil {
			ev.Entry.Sidechain = sidechain
		}
		if err := c.record(ev); err != nil {
			return err
		}
	}
	return nil
}

// messageEntries maps one message's content blocks to canonical entries.
// role selects EntryTypeUser vs EntryTypeAssistant for buffered text; isMeta
// (only ever meaningful for role=="user") skips the whole line when true —
// claude's own injected wrapper/caveat text, never something the human
// typed, mirroring codex's developer-role skip (rollout.go's messageEvents
// doc comment) for the same reason.
//
// Consecutive "text" blocks are buffered and joined into ONE entry (like
// codex's joinContentText) rather than one entry per block, since the common
// real shape is either a single text block or several text blocks that
// together form one coherent turn (a multi-part system-reminder + the actual
// prompt, observed on this box). Every other block type — thinking,
// tool_use, tool_result — flushes any buffered text first and then becomes
// its own entry, since those never co-occur with text in the same content
// array on this box's captured transcripts; processing in encounter order
// rather than assuming a fixed grouping keeps this correct even if that ever
// changes.
func messageEntries(role string, isMeta bool, blocks []contentBlock) []agent.ChatEvent {
	if role == "user" && isMeta {
		return nil
	}

	entryType := agent.EntryTypeUser
	if role == "assistant" {
		entryType = agent.EntryTypeAssistant
	}

	var evs []agent.ChatEvent
	var textParts []string
	flushText := func() {
		if len(textParts) == 0 {
			return
		}
		text := strings.Join(textParts, "\n\n")
		textParts = nil
		if text == "" {
			return
		}
		evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{Type: entryType, Content: text}})
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "thinking":
			flushText()
			if b.Thinking != "" {
				evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: b.Thinking}})
			}
		case "tool_use":
			flushText()
			evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:       agent.EntryTypeToolUse,
				ToolName:   b.Name,
				ToolInput:  nonEmptyRaw(b.Input),
				ToolCallID: b.ID,
			}})
		case "tool_result":
			flushText()
			evs = append(evs, agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:       agent.EntryTypeToolResult,
				ToolOutput: toolResultText(b.Content),
				ToolCallID: b.ToolUseID,
				IsError:    b.IsError,
			}})
		default:
			// An unmodeled/future block type (e.g. "image" pasted directly
			// into a user turn — observed but rare on this box): skip, not
			// fatal, same as codex's unrecognized response_item variant.
		}
	}
	flushText()
	return evs
}

// toolResultText flattens a tool_result block's own (dual-shaped) content
// into the flat string agent.SessionEntry.ToolOutput expects: a bare string
// passes through directly (via decodeContentBlocks' string-wrapping), and an
// array joins only its "text"-type elements. "tool_reference" and "image"
// elements (observed inside real tool_result content arrays on this box —
// tool_reference names an available tool the model was pointed at,  image
// carries binary bytes this schema has no field for) are DROPPED here, not
// fabricated into text — an honest gap, matching codex's own
// function_call_output "no error field, don't sniff prose" discipline.
func toolResultText(raw json.RawMessage) string {
	blocks := decodeContentBlocks(raw)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// nonEmptyRaw normalizes a zero-length json.RawMessage to nil so
// agent.SessionEntry.ToolInput's omitempty (record.go's EntryPayload.
// ToolInput) actually omits it, rather than round-tripping an empty-but-non-nil
// slice that json.RawMessage's own MarshalJSON would otherwise turn into a
// literal `null`.
func nonEmptyRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
