package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
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
	PermissionMode string   `json:"permissionMode"`
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

// convertLines runs the two-pass conversion via importer.ConvertJSONLLines,
// mirroring codex's convertLines: scanSessionInfo first (one Session
// ChatEvent recorded once up front), then a streamed second pass in the
// file's own order for every user/assistant line.
// The two-pass shape (scan once, then stream) is the reference pattern every
// importer.VendorAdapter copies from codex on purpose (codex.go's package
// doc). What genuinely differs between codex and claude here — no envelope,
// dual-shaped content, id-keyed accounting vs a second event type — stays in
// each vendor's own dispatch; what doesn't differ (line reading, latching
// session-info fields, joining text blocks, shaping a tool_use/tool_result
// entry, flushing a pending Complete boundary, and now the outer scan/
// stream/flush shell itself) lives once in the importer package both
// packages import, not copied here.
func convertLines(ctx context.Context, rec transcript.Recorder, lines [][]byte) error {
	c := &converter{record: importer.RecordFunc(rec, "claude")}
	err := importer.ConvertJSONLLines(ctx, rec, lines, "claude", scanSessionInfo(lines),
		func(raw []byte) error {
			var l line
			if err := json.Unmarshal(raw, &l); err != nil {
				c.malformed++
				return nil // malformed line: skip, never fatal (see importer.VendorAdapter doc)
			}
			switch l.Type {
			case "user":
				c.conversational++
				return c.handleUser(l)
			case "assistant":
				c.conversational++
				return c.handleAssistant(l)
			}
			// Every other Type (progress, queue-operation, system,
			// attachment, last-prompt, mode, permission-mode, ai-title,
			// custom-title, file-history-snapshot, agent-name, pr-link,
			// worktree-state, file-history-delta, agent-color) contributes no
			// entries: see line's doc comment. Those are ADMINISTRATIVE, so
			// contributing nothing is correct and silent — but a type this
			// build has never heard of is vendor content going on the floor,
			// and gets counted so the operator hears about it (U147-F03).
			if !adminLineTypes[l.Type] {
				c.drops.add("line:" + l.Type)
			}
			return nil
		},
		c.flushPending)
	if err != nil {
		return err
	}
	c.reportDrops()
	return c.checkFloor(len(lines))
}

// adminLineTypes is claude's own UI/session bookkeeping — line types that
// legitimately carry no conversational content (see line's doc comment).
// Anything OUTSIDE this set that isn't user/assistant is unknown to this
// build, which is a different thing entirely and is reported.
var adminLineTypes = map[string]bool{
	"progress": true, "queue-operation": true, "system": true,
	"attachment": true, "last-prompt": true, "mode": true,
	"permission-mode": true, "ai-title": true, "custom-title": true,
	"file-history-snapshot": true, "agent-name": true, "pr-link": true,
	"worktree-state": true, "file-history-delta": true, "agent-color": true,
	"summary": true, "x-ctxloom-meta": true,
}

// checkFloor is the answer to "can this importer produce zero entries and
// still report success?" (U147-F01). It can — legitimately — for a transcript
// that holds nothing but administrative lines, and that stays a success. What
// must NOT stay a success is zero entries out of a file that DID contain
// lines this adapter claims to understand, or whose lines all failed to
// parse: that is a failed import wearing a success's clothes, and every layer
// above (ConvertVendorTranscript, `session backfill`, the pty exit seam) is
// structurally unable to tell the difference on its own.
func (c *converter) checkFloor(total int) error {
	if c.entries > 0 {
		return nil
	}
	switch {
	case c.conversational > 0:
		return fmt.Errorf("claude: read %d lines including %d user/assistant lines but converted ZERO transcript entries — the vendor format this build parses no longer matches the file", total, c.conversational)
	case c.malformed > 0 && c.malformed == total:
		return fmt.Errorf("claude: all %d lines failed to parse as JSON — not a transcript this build can read", total)
	case c.malformed > 0:
		return fmt.Errorf("claude: converted ZERO transcript entries from %d lines (%d of them malformed)", total, c.malformed)
	}
	return nil
}

// reportDrops tells the operator what vendor content this build could not
// represent. Dropping is the honest outcome for a block type ctxloom's
// canonical schema has no field for — dropping it SILENTLY is not (U147-F03).
func (c *converter) reportDrops() {
	if c.drops.total() == 0 {
		return
	}
	agent.Warn("claude transcript import: dropped %d vendor content item(s) with no canonical representation (%s)", c.drops.total(), c.drops.summary())
}

// dropTally counts, by a short label, vendor content that went on the floor.
// nil-safe so the pure mapping helpers can be called without one.
type dropTally struct{ counts map[string]int }

func (d *dropTally) add(label string) {
	if d == nil {
		return
	}
	if d.counts == nil {
		d.counts = make(map[string]int)
	}
	d.counts[label]++
}

func (d *dropTally) total() int {
	if d == nil {
		return 0
	}
	n := 0
	for _, v := range d.counts {
		n += v
	}
	return n
}

// summary renders the tally as a stable, sorted "label×N, label×N" string.
func (d *dropTally) summary() string {
	if d == nil || len(d.counts) == 0 {
		return ""
	}
	labels := make([]string, 0, len(d.counts))
	for k := range d.counts {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	parts := make([]string, 0, len(labels))
	for _, l := range labels {
		parts = append(parts, fmt.Sprintf("%s×%d", l, d.counts[l]))
	}
	return strings.Join(parts, ", ")
}

// scanSessionInfo makes one pass over every line looking for session-level
// metadata, latching each field onto its FIRST occurrence only via
// importer.SessionInfoBuilder — mirrors codex's scanSessionInfo
// (rollout.go). Returns nil if the file contained none of it at all.
func scanSessionInfo(lines [][]byte) *agent.ChatSessionInfo {
	var b importer.SessionInfoBuilder
	for _, raw := range lines {
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			continue
		}
		b.SetSessionID(l.SessionID)
		if l.Type == "assistant" && l.Message != nil {
			b.SetModel(l.Message.Model)
		}
		if l.Type == "user" {
			b.SetPermissionMode(l.PermissionMode)
		}
	}
	return b.Build()
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
	record    func(agent.ChatEvent) error
	pending   *agent.TurnMeta
	pendingID string

	// Import accounting, read by checkFloor/reportDrops once the stream is
	// done: how many lines this adapter claimed to understand, how many it
	// could not parse at all, how many canonical entries actually came out,
	// and what vendor content was dropped along the way.
	conversational int
	malformed      int
	entries        int
	drops          dropTally
}

// flushPending records a still-open Complete boundary at end of file — a
// response whose accounting was captured but whose message.id never changed
// again before the file ended (a truncated/interrupted transcript, e.g. the
// user cancelled mid-turn). A nil pending is a normal, silent no-op.
// pendingID is reset unconditionally: it and pending are always set/cleared
// together (see handleAssistant), so when pending is nil, pendingID is
// already "". See importer.FlushComplete for the flush mechanics shared with
// codex's identical boundary-flush shape.
func (c *converter) flushPending() error {
	c.pendingID = ""
	return importer.FlushComplete(&c.pending, c.record)
}

// handleUser dispatches one "user"-type line's content blocks to canonical
// entries.
func (c *converter) handleUser(l line) error {
	if l.Message == nil {
		return nil
	}
	blocks := decodeContentBlocks(l.Message.Content)
	evs := messageEntries("user", l.IsMeta, blocks, &c.drops)
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
	evs := messageEntries("assistant", false, blocks, &c.drops)
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
			c.entries++
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
func messageEntries(role string, isMeta bool, blocks []contentBlock, drops *dropTally) []agent.ChatEvent {
	// isMeta can only ever be true on the "user" call path — handleAssistant
	// (below) hardcodes isMeta=false at its call site, so the role=="user"
	// half of this guard was always true whenever isMeta was (U147-F08).
	if isMeta {
		return nil
	}

	entryType := agent.EntryTypeUser
	if role == "assistant" {
		entryType = agent.EntryTypeAssistant
	}

	var evs []agent.ChatEvent
	var textParts []string
	flushText := func() {
		evs = append(evs, importer.TextEntry(entryType, importer.JoinNonEmpty(textParts))...)
		textParts = nil
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "thinking":
			flushText()
			evs = append(evs, importer.TextEntry(agent.EntryTypeThinking, b.Thinking)...)
		case "tool_use":
			flushText()
			evs = append(evs, importer.ToolUseEvent(b.Name, b.ID, b.Input))
		case "tool_result":
			flushText()
			evs = append(evs, importer.ToolResultEvent(b.ToolUseID, toolResultText(b.Content, drops), b.IsError))
		default:
			// An unmodeled/future block type (e.g. "image" pasted directly
			// into a user turn — observed but rare on this box): skip, not
			// fatal, same as codex's unrecognized response_item variant. It is
			// COUNTED, though — a drop nobody can observe is indistinguishable
			// from content that was never there (U147-F03).
			drops.add("block:" + b.Type)
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
func toolResultText(raw json.RawMessage, drops *dropTally) string {
	blocks := decodeContentBlocks(raw)
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
			continue
		}
		drops.add("tool_result:" + b.Type)
	}
	return importer.JoinNonEmpty(parts)
}
