package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
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
	// ToolUseResult is claude's TOP-LEVEL structured result for the
	// tool_result block carried in this same line's message content — the
	// richer counterpart of that block's flattened text (a Bash call's
	// {stdout,stderr,interrupted}, an Edit's {originalFile,structuredPatch},
	// a ToolSearch's {matches,query,total_deferred_tools}).
	//
	// Left raw and classified by shape in toolUseResultBlocks. Verified 1:1
	// with the tool_result block: all 15821 lines carrying this key have
	// exactly one tool_result in message.content, so it folds into that
	// entry rather than becoming an entry of its own.
	ToolUseResult json.RawMessage `json:"toolUseResult,omitempty"`
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
	// Raw is this element's own bytes, verbatim, populated by
	// decodeContentBlocks rather than by the json tags. It is what lets an
	// element this struct does not model survive canonicalization instead of
	// going on the floor — see toolResultContent. Skipped by encoding/json on
	// the way in ("-") because it is filled in afterwards, from the outside.
	Raw json.RawMessage `json:"-"`
}

// decodeContentBlocks normalizes claude's dual-shaped content field (bare
// JSON string | array of block objects — see message.Content and
// contentBlock.Content's doc comments) into one slice. A bare string becomes
// a single synthetic {Type: "text", Text: <the string>} block, so every
// caller — the top-level message content AND a tool_result's own nested
// content — handles exactly one shape. Returns nil for empty/malformed raw
// rather than erroring: an unparseable content field degrades to "no
// entries from this line," never aborts the whole conversion (vendorreader.
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
		return []contentBlock{{Type: "text", Text: s, Raw: raw}}
	}
	// Decoded element-at-a-time rather than straight into []contentBlock so
	// each block keeps its OWN bytes in Raw. That is what makes
	// canonicalization total: an element type this struct does not model is
	// still carried verbatim instead of being silently flattened away.
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil
	}
	blocks := make([]contentBlock, 0, len(elems))
	for _, e := range elems {
		var b contentBlock
		if err := json.Unmarshal(e, &b); err != nil {
			// Unparseable element: keep the bytes under an empty Type, which
			// canonicalizes to the generic kind. Degrade-to-partial, never
			// abort (vendorreader.VendorAdapter's contract).
			blocks = append(blocks, contentBlock{Raw: e})
			continue
		}
		b.Raw = e
		blocks = append(blocks, b)
	}
	return blocks
}

// convertLines runs the two-pass conversion via vendorreader.ConvertJSONLLines,
// mirroring codex's convertLines: scanSessionInfo first (one Session
// ChatEvent recorded once up front), then a streamed second pass in the
// file's own order for every user/assistant line.
// The two-pass shape (scan once, then stream) is the reference pattern every
// vendorreader.VendorAdapter copies from codex on purpose (codex.go's package
// doc). What genuinely differs between codex and claude here — no envelope,
// dual-shaped content, id-keyed accounting vs a second event type — stays in
// each vendor's own dispatch; what doesn't differ (line reading, latching
// session-info fields, joining text blocks, shaping a tool_use/tool_result
// entry, flushing a pending Complete boundary, and now the outer scan/
// stream/flush shell itself) lives once in the reader package both
// packages import, not copied here.
func convertLines(ctx context.Context, rec transcript.Recorder, lines [][]byte) error {
	c := &converter{record: vendorreader.RecordFunc(rec, "claude")}
	err := vendorreader.ConvertJSONLLines(ctx, rec, lines, "claude", scanSessionInfo(lines),
		func(raw []byte) error {
			var l line
			if err := json.Unmarshal(raw, &l); err != nil {
				c.malformed++
				return nil // malformed line: skip, never fatal (see vendorreader.VendorAdapter doc)
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
			// and gets counted so the operator hears about it.
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

// checkFloor is the answer to "can this reader produce zero entries and
// still report success?" It can — legitimately — for a transcript
// that holds nothing but administrative lines, and that stays a success. What
// must NOT stay a success is zero entries out of a file that DID contain
// lines this adapter claims to understand, or whose lines all failed to
// parse: that is a failed import wearing a success's clothes, and every layer
// above (ConvertVendorTranscript, the recover_session tool, the pty exit seam) is
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
// canonical schema has no field for — dropping it SILENTLY is not.
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

// scanSessionInfo scans for session-level metadata, latching each field onto
// its FIRST occurrence only via vendorreader.SessionInfoBuilder — mirrors codex's
// scanSessionInfo (rollout.go). Returns nil if the file contained none of it
// at all.
//
// It stops as soon as all three fields are latched. Because latching is
// first-occurrence-only, nothing after that point can change the result, so
// the early exit is behaviour-preserving by construction — and it is worth
// having: this is the FIRST of two decodes every line of the file undergoes
// (convertLines then decodes each line again into this same struct). Measured
// on the eight largest real claude transcripts on this box — 5.9 to 6.5 MB,
// 2100 to 3100 lines each — all three fields were latched by line 23 to 113,
// so the second decode drops from every line to roughly the first two dozen.
//
// A transcript that never carries one of the fields (permissionMode rides
// only on a genuinely human-typed user line, so an entirely synthetic one has
// none) still scans to the end, which is correct: the field could be on the
// last line. That case, and the whole file being held in memory as [][]byte
// by vendorreader.OpenAndReadJSONLLines, are the parts of the double-decode cost
// this cannot address from inside one adapter.
func scanSessionInfo(lines [][]byte) *agent.ChatSessionInfo {
	var b vendorreader.SessionInfoBuilder
	var scan sessionScan
	for _, raw := range lines {
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			continue
		}
		if scan.observe(&b, l) {
			break
		}
	}
	return b.Build()
}

// sessionScan tracks which of the three session-level fields claude's format
// can supply have been latched, so the scan knows when it is done.
type sessionScan struct{ id, model, mode bool }

// observe latches l's session-level fields into b and reports whether every
// field is now latched — the point after which no later line can change the
// result, since SessionInfoBuilder keeps only first occurrences.
func (s *sessionScan) observe(b *vendorreader.SessionInfoBuilder, l line) bool {
	if l.SessionID != "" {
		b.SetSessionID(l.SessionID)
		s.id = true
	}
	switch l.Type {
	case "assistant":
		if l.Message != nil && l.Message.Model != "" {
			b.SetModel(l.Message.Model)
			s.model = true
		}
	case "user":
		if l.PermissionMode != "" {
			b.SetPermissionMode(l.PermissionMode)
			s.mode = true
		}
	}
	return s.id && s.model && s.mode
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
// already "". See vendorreader.FlushComplete for the flush mechanics shared with
// codex's identical boundary-flush shape.
func (c *converter) flushPending() error {
	c.pendingID = ""
	return vendorreader.FlushComplete(&c.pending, c.record)
}

// handleUser dispatches one "user"-type line's content blocks to canonical
// entries.
func (c *converter) handleUser(l line) error {
	if l.Message == nil {
		return nil
	}
	blocks := decodeContentBlocks(l.Message.Content)
	evs := messageEntries("user", l.IsMeta, blocks, l.ToolUseResult, &c.drops)
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
	evs := messageEntries("assistant", false, blocks, l.ToolUseResult, &c.drops)
	if err := c.recordAll(evs, l.IsSidechain); err != nil {
		return err
	}

	if l.Message.Usage == nil {
		return nil
	}
	// A pending boundary is closed whenever the incoming line's identity is
	// not the pending one's — INCLUDING when either id is absent. Requiring
	// both to be non-empty made an id-less usage line fall through to the
	// unconditional overwrite below, discarding a Complete record that had
	// already been fully assembled, with no error and nothing said.
	// Comparing the ids (rather than flushing on every usage line) is what
	// keeps the several lines of ONE response folding into ONE boundary — see
	// converter's doc comment.
	if c.pending != nil && l.Message.ID != c.pendingID {
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
// toolUseResult is the line's own top-level structured result, folded into
// the tool_result entry this content produces (see line.ToolUseResult). Nil
// on every line that carries no tool_result.
func messageEntries(role string, isMeta bool, blocks []contentBlock, toolUseResult json.RawMessage, drops *dropTally) []agent.ChatEvent {
	// isMeta can only ever be true on the "user" call path — handleAssistant
	// (below) hardcodes isMeta=false at its call site, so the role=="user"
	// half of this guard was always true whenever isMeta was.
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
		evs = append(evs, vendorreader.TextEntry(entryType, vendorreader.JoinNonEmpty(textParts))...)
		textParts = nil
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "thinking":
			flushText()
			evs = append(evs, vendorreader.TextEntry(agent.EntryTypeThinking, b.Thinking)...)
		case "tool_use":
			flushText()
			evs = append(evs, vendorreader.ToolUseEvent(b.Name, b.ID, b.Input))
		case "tool_result":
			flushText()
			text, content := toolResultContent(b.Content)
			// The line's structured result belongs to THIS entry: appended
			// after the block's own elements so the flattened text and the
			// elements behind it stay in encounter order.
			content = append(content, toolUseResultBlocks(toolUseResult)...)
			evs = append(evs, vendorreader.ToolResultEvent(b.ToolUseID, text, b.IsError, content))
		default:
			// An unmodeled/future block type (e.g. "image" pasted directly
			// into a user turn — observed but rare on this box): skip, not
			// fatal, same as codex's unrecognized response_item variant. It is
			// COUNTED, though — a drop nobody can observe is indistinguishable
			// from content that was never there.
			drops.add("block:" + b.Type)
		}
	}
	flushText()
	return evs
}

// toolResultContent canonicalizes a tool_result block's own (dual-shaped)
// content TOTALLY: every element becomes a canonical block, and the "text"
// ones additionally join into the flat string agent.SessionEntry.ToolOutput
// expects.
//
// It takes no *dropTally because it drops nothing. That is the layer-1 rule:
// an adapter canonicalizes, it does not decide what is worth keeping — that
// is internal/transcript/policy's job, one layer up, where the decision is
// written down once and reads the same for every engine.
//
// The predecessor (toolResultText) kept only type:"text" and tallied the rest
// as having "no canonical representation", which was simply false —
// agent.ToolContentBlock.Raw is documented as carrying "the element verbatim
// regardless, for anything this type doesn't otherwise model", and
// transcript.toolContentPayload already serializes it. Measured over 1030
// claude transcripts: 677 tool_reference + 1 image elements discarded, and
// 385 tool_result entries flattened to COMPLETELY EMPTY across 145 files —
// entries that read as "this tool returned nothing", which is this project's
// characteristic silent-no-op shape, written deliberately.
func toolResultContent(raw json.RawMessage) (text string, blocks []agent.ToolContentBlock) {
	elems := decodeContentBlocks(raw)
	if len(elems) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(elems))
	blocks = make([]agent.ToolContentBlock, 0, len(elems))
	for _, e := range elems {
		switch e.Type {
		case "text":
			parts = append(parts, e.Text)
			blocks = append(blocks, agent.ToolContentBlock{
				Kind: agent.KindContent, Text: e.Text, Raw: e.Raw,
			})
		case "tool_reference":
			// A listing of tools the model was pointed at. Named by purpose so
			// the policy layer never has to know the vendor spelling.
			blocks = append(blocks, agent.ToolContentBlock{
				Kind: agent.KindToolCatalog, Raw: e.Raw,
			})
		default:
			// Everything else — image, and whatever claude ships next — is
			// preserved verbatim in Raw under the generic kind rather than
			// discarded. An unnamed element is not a lost one.
			blocks = append(blocks, agent.ToolContentBlock{
				Kind: agent.KindContent, Raw: e.Raw,
			})
		}
	}
	return vendorreader.JoinNonEmpty(parts), blocks
}

// toolUseResultBlocks canonicalizes claude's top-level "toolUseResult" key —
// a vendor field previously parsed NOT AT ALL, measured at 11.0 MB over 4378
// records in the last 40 sessions of this project alone. It rides on the same
// line as the tool_result block it belongs to (verified: all 15821
// toolUseResult lines carry exactly one tool_result block), so it is folded
// into that entry's content rather than becoming an entry of its own.
//
// Classification is BY SHAPE — which keys are present — not by tool name.
// Two reasons, and the second is the load-bearing one:
//   - the converter keeps no tool_use_id -> tool-name map, so a name-based
//     rule would need correlation state invented purely to classify;
//   - a shape survives a tool being renamed or a new tool arriving with the
//     same result shape, and naming vendor keys is precisely what an ADAPTER
//     is for. The prohibition on naming vendor fields binds the policy layer,
//     not this one.
//
// Shapes below are every one observed at scale in this project's own
// transcripts; anything unrecognized keeps its bytes under the generic kind.
func toolUseResultBlocks(raw json.RawMessage) []agent.ToolContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		// A scalar toolUseResult (2297 bare strings measured) or an array (41):
		// no shape to classify, but the bytes are still real content.
		return []agent.ToolContentBlock{{Kind: agent.KindContent, Raw: raw}}
	}
	has := func(names ...string) bool {
		for _, n := range names {
			if _, ok := keyed[n]; !ok {
				return false
			}
		}
		return true
	}
	kind := agent.KindContent
	switch {
	case has("stdout", "stderr", "interrupted"):
		kind = agent.KindProcessOutput
	case has("originalFile", "structuredPatch"):
		kind = agent.KindFileSnapshot
	case has("total_deferred_tools"):
		kind = agent.KindToolCatalog
	case has("agentId", "status"):
		kind = agent.KindAgentResult
	}
	return []agent.ToolContentBlock{{Kind: kind, Raw: raw}}
}
