// Package transcript owns ctxloom's OWN captured conversation record — the
// canonical transcript.jsonl file under a harp's persist/ dir
// (paths.HarpCanonicalTranscriptPath). It is the runner-side alternative to
// scraping each engine's private, version-unstable session-store file (see
// docs/transcript-schema.md for the full design rationale).
//
// This file (record.go) defines the ON-DISK SCHEMA: one JSON object per JSONL
// line, an envelope (v/harp/session_id/engine/seq/ts/kind) wrapping exactly one
// populated payload matching Kind, mirroring agent.ChatEvent's four variants
// field-for-field — because that type is ALREADY the semantic union every
// structured engine (codex, kiro, claude-via-acp, opencode, generic acp)
// normalizes onto via internal/acp/mapping.go before ctxloom ever sees a wire
// frame. This package does not invent a new vocabulary; it adds an
// append-only, ordered, harp-addressed envelope around the one that already
// exists. See recorder.go for the writer (Recorder) and Tee.
package transcript

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// SchemaVersion is the canonical transcript envelope's current version. Bump
// on any breaking change to the envelope or payload shapes. A reader
// encountering a Record.V it does not recognize must fail loud rather than
// guess at the shape (memory "isolation-must-not-negotiate": fail loud, never
// silently mis-parse) — enforced by the S3 CanonicalHistory reader, not here.
const SchemaVersion = 1

// Kind discriminates which payload field of a Record is populated. Exactly
// one of Entry/Session/Complete/Permission is non-nil for a given Kind.
type Kind string

const (
	// KindEntry carries one atomic piece of conversation CONTENT — the turn's
	// substance (assistant text, thinking, tool_use, tool_result, etc). MANY
	// per response. Mirrors agent.ChatEvent.Entry.
	KindEntry Kind = "entry"
	// KindSession carries one-time session metadata (model, permission mode,
	// context window, MCP server status), emitted once near the start of a
	// conversation. Mirrors agent.ChatEvent.Session.
	KindSession Kind = "session"
	// KindComplete carries a response's completion accounting (tokens, cost,
	// duration, stop reason) — a turn BOUNDARY marker with NO content. Mirrors
	// agent.ChatEvent.Complete.
	KindComplete Kind = "complete"
	// KindPermission carries a forwarded engine permission request awaiting a
	// caller decision. Mirrors agent.ChatEvent.Permission.
	KindPermission Kind = "permission"
	// KindRaw carries ONLY the IR3 raw side channel (Record.Raw) — a
	// ChatEvent that had no Entry/Session/Complete/Permission of its own
	// (e.g. an available_commands_update/current_mode_update passthrough
	// forwarded via agent.ChatEvent.Raw; see internal/acp/mapping.go's
	// rawOnlyEvent). Distinct from the other four Kinds carrying no payload:
	// this one is legitimately payload-FREE of the structured kinds by
	// construction, not an error.
	KindRaw Kind = "raw"
)

// Record is one line of the canonical transcript.jsonl file.
type Record struct {
	// V is the schema version this line was written under (SchemaVersion at
	// write time). Never omitted — an absent v is itself a format error.
	V int `json:"v"`
	// Harp is the ctxloom session id — the authoritative key. Every line in a
	// given transcript.jsonl file carries the same harp (the file lives
	// under that harp's persist/ dir), but the field rides each line anyway so
	// a line is self-describing if ever extracted from its file.
	Harp string `json:"harp"`
	// SessionID is the engine-NATIVE ACP session id this conversation runs
	// under (agent.ChatEvent.Session.SessionID / agent.ChatSessionInfo.
	// SessionID) — the resume handle. Empty until the KindSession line has
	// been seen (or if the backend exposes none), so early lines in a
	// transcript may have it blank; once known, the recorder carries it
	// forward onto every subsequent line so any single line remains
	// self-describing.
	SessionID string `json:"session_id,omitempty"`
	// Engine carries the driving backend's REGISTERED name verbatim, exactly
	// as NewRecorder received it: nothing on this path normalizes, allowlists
	// or refuses a value. That name comes from the backend registry
	// (internal/lm/backends), which is NOT the vocabulary of the `engine`
	// enum published in docs/transcript.schema.json, and the two disagree —
	// claude registers as "claude-code" and the test backend as "mock",
	// neither of which the enum admits. So a claude line written here does
	// not validate against the shipped schema. Which vocabulary is canonical
	// is an open contract decision; it cannot be settled by normalizing here,
	// because "mock" has no enum member to normalize onto.
	//
	// antigravity's lines do not arrive through the structured tee: its
	// StructuredChat is a bespoke prose driver over `agy -p` rather than
	// internal/acp.NewChatDriver, so its canonical lines come from the
	// oneshot/reader regimes (plan §2d), which stamp Engine the same way.
	Engine string `json:"engine"`
	// Seq is monotonically increasing per transcript, starting at 0, with NO
	// gaps — the ordering key. A reader can detect truncation/corruption by a
	// seq discontinuity.
	Seq int `json:"seq"`
	// TS is the RFC3339 UTC RECEIPT time (when the recorder observed the
	// event), not any engine-reported timestamp — engines vary in whether/how
	// they timestamp individual frames, so this is the one clock every line
	// can trust.
	TS time.Time `json:"ts"`
	// Kind selects which payload field below is populated.
	Kind Kind `json:"kind"`

	Entry      *EntryPayload      `json:"entry,omitempty"`
	Session    *SessionPayload    `json:"session,omitempty"`
	Complete   *CompletePayload   `json:"complete,omitempty"`
	Permission *PermissionPayload `json:"permission,omitempty"`

	// Raw is an OPTIONAL escape hatch for the original engine frame. As of IR3
	// (2026-07), agent.ChatEvent DOES carry one (ChatEvent.Raw — the protocol
	// side channel for the curated available_commands_update/
	// current_mode_update/_meta allowlist) and the Recorder in THIS package
	// DOES populate this field FROM it, gated by RawPolicy (see NewRecorder's
	// WithRawPolicy option). This is a CAPTURE-layer decision only — it
	// governs what gets written to DISK from a ChatEvent.Raw that already
	// exists (or doesn't) by the time a Recorder sees it; it has nothing to
	// do with what crosses the wire (that is entirely internal/acp's and
	// internal/acpagent's mapping-layer allowlist). Never conflate protocol
	// `_meta`/passthrough forwarding with this transcript capture policy —
	// they are unrelated decisions that happen to share a name ("raw").
	Raw json.RawMessage `json:"raw,omitempty"`
}

// EntryPayload is the KindEntry payload — one agent.SessionEntry, field for
// field (minus Timestamp, which is the envelope's TS).
type EntryPayload struct {
	// Type is the agent.SessionEntryType string: user|assistant|thinking|
	// tool_use|tool_result|system.
	Type       string          `json:"type"`
	Content    string          `json:"content,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	// Sidechain marks an entry belonging to an engine's own in-harness
	// subagent rather than the session's main thread (agent.SessionEntry.
	// Sidechain / agent.MainThreadEntries).
	Sidechain bool `json:"sidechain,omitempty"`

	// --- IR2 (2026-07): mirrors agent.SessionEntry's additive richness
	// fields, field for field. All optional/omitempty, same as above.
	ToolCallID    string             `json:"tool_call_id,omitempty"`
	ToolKind      string             `json:"tool_kind,omitempty"`
	ToolLocations []ToolLocation     `json:"tool_locations,omitempty"`
	ToolContent   []ToolContentBlock `json:"tool_content,omitempty"`
	ContentBlocks []ContentBlock     `json:"content_blocks,omitempty"`
	SystemKind    string             `json:"system_kind,omitempty"`
	Plan          []PlanEntry        `json:"plan,omitempty"`
}

// ToolLocation mirrors agent.ToolLocation.
type ToolLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// ContentBlock mirrors agent.ContentBlock.
type ContentBlock struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// ToolContentBlock mirrors agent.ToolContentBlock.
type ToolContentBlock struct {
	Kind        string          `json:"kind"`
	Text        string          `json:"text,omitempty"`
	DiffPath    string          `json:"diff_path,omitempty"`
	DiffOldText string          `json:"diff_old_text,omitempty"`
	DiffNewText string          `json:"diff_new_text,omitempty"`
	TerminalID  string          `json:"terminal_id,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// PlanEntry mirrors agent.PlanEntry.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// SessionPayload is the KindSession payload — agent.ChatSessionInfo minus
// SessionID (hoisted to the envelope).
type SessionPayload struct {
	// Resumable mirrors agent.ChatSessionInfo.Resumable: the LIVE half of the
	// one-shot resume gate (the connected adapter's own handshake advertising
	// session/load), as distinct from the static per-backend capability
	// table. Without it on disk, a transcript cannot answer "could this
	// native session have been resumed?" after the fact.
	Resumable      bool        `json:"resumable,omitempty"`
	Model          string      `json:"model,omitempty"`
	PermissionMode string      `json:"permission_mode,omitempty"`
	ContextWindow  int         `json:"context_window,omitempty"`
	MCPServers     []MCPStatus `json:"mcp_servers,omitempty"`
}

// MCPStatus is the connection status of one MCP server at session start
// (agent.MCPStatus).
type MCPStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CompletePayload is the KindComplete payload — agent.TurnMeta, carried in
// FULL (every field): this schema is a LOSSLESS superset, not a trimmed
// projection, so a field ctxloom doesn't display today is still on disk for a
// later consumer.
type CompletePayload struct {
	InputTokens         int     `json:"input_tokens,omitempty"`
	OutputTokens        int     `json:"output_tokens,omitempty"`
	CacheReadTokens     int     `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int     `json:"cache_creation_tokens,omitempty"`
	ContextWindow       int     `json:"context_window,omitempty"`
	MaxOutputTokens     int     `json:"max_output_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
	Model               string  `json:"model,omitempty"`
	StopReason          string  `json:"stop_reason,omitempty"`
	DurationMs          int     `json:"duration_ms,omitempty"`
	NumTurns            int     `json:"num_turns,omitempty"`
}

// PermissionPayload is the KindPermission payload — agent.PermissionRequest,
// field for field.
type PermissionPayload struct {
	ID        string             `json:"id"`
	ToolName  string             `json:"tool_name,omitempty"`
	ToolInput json.RawMessage    `json:"tool_input,omitempty"`
	Options   []PermissionOption `json:"options,omitempty"`
	// Kind is the connector-classified tool category (agent.PermissionRequest.
	// Kind), when the backend's native protocol supplies one. Distinct from
	// Record.Kind (the envelope discriminator) — this is the ACP ToolCallKind
	// vocabulary ("execute"|"edit"|"delete"|"move"|"read"|"search"|"fetch"|
	// "think"|"other"), advisory metadata about the tool being requested.
	Kind string `json:"kind,omitempty"`
	// ToolCallID mirrors agent.PermissionRequest.ToolCallID — the
	// engine-native tool-call id this request refers to. Without it a
	// recorded permission request cannot be re-paired with the tool call it
	// gated, leaving only name-based guessing. The published
	// schema (docs/transcript.schema.json) already declared this key before
	// the Go struct carried it.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// PermissionOption is one decision the engine offers for a permission request
// (agent.PermissionOption).
type PermissionOption struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// payloadFromChatEvent classifies ev and builds its Kind + payload, leaving
// the envelope fields (v/harp/session_id/engine/seq/ts) for the caller
// (Recorder.Record) to stamp. Returns an error for a truly zero-value
// ChatEvent (no variant AND no Raw) — the caller must not silently emit a
// line with no payload; a blank envelope masquerading as a recorded event is
// exactly the silent-no-op failure mode this package exists to avoid. A
// raw-only event (Entry/Session/Complete/Permission all nil, Raw non-empty —
// IR3's passthrough events) is NOT that case: it legitimately has nothing
// else to say, and classifies as KindRaw instead of erroring.
func payloadFromChatEvent(ev agent.ChatEvent) (Kind, *EntryPayload, *SessionPayload, *CompletePayload, *PermissionPayload, error) {
	switch {
	case ev.Entry != nil:
		return KindEntry, entryPayload(ev.Entry), nil, nil, nil, nil
	case ev.Session != nil:
		return KindSession, nil, sessionPayload(ev.Session), nil, nil, nil
	case ev.Complete != nil:
		return KindComplete, nil, nil, completePayload(ev.Complete), nil, nil
	case ev.Permission != nil:
		return KindPermission, nil, nil, nil, permissionPayload(ev.Permission), nil
	case len(ev.Raw) > 0:
		return KindRaw, nil, nil, nil, nil, nil
	default:
		return "", nil, nil, nil, nil, fmt.Errorf("transcript: ChatEvent carries no variant and no Raw (Entry/Session/Complete/Permission all nil, Raw empty) — refusing to record an empty line")
	}
}

// RawPolicy controls whether/when the Recorder persists a ChatEvent's IR3 raw
// protocol side channel (agent.ChatEvent.Raw) into Record.Raw. This is a
// CAPTURE-layer policy ONLY: by the time a Recorder ever sees a ChatEvent,
// ev.Raw already exists (or doesn't) — the mapping layer
// (internal/acp/mapping.go, internal/acpagent/mapping.go) decided that; this
// policy cannot make the hub carry MORE than the mapping layer already chose
// to forward, and in particular can never surface a permission request here
// — permissions are never carried on ChatEvent.Raw in the first place (see
// that field's doc comment).
type RawPolicy string

const (
	// RawOff never persists Raw. A raw-ONLY event (nothing else to record)
	// is dropped from the transcript entirely under this policy, rather than
	// writing a payload-free placeholder line — the same "write nothing,
	// verifiably" discipline NewRecorder's lazy-open behavior follows.
	RawOff RawPolicy = "off"
	// RawLossyOnly (the DEFAULT) persists Raw only when it is the event's
	// SOLE payload — the mapping layer had no other IR projection for this
	// frame at all (a raw-only passthrough event, KindRaw). When Raw merely
	// SUPPLEMENTS an already-captured Entry/Session/Complete/Permission
	// (e.g. a `_meta` blob riding alongside a mapped tool_call), it is
	// dropped: that structured payload is not itself lossy, and the
	// supplement is treated as non-essential to keep on disk by default.
	RawLossyOnly RawPolicy = "lossy-only"
	// RawAll persists Raw whenever the mapping layer set it, even alongside
	// an already-fully-captured structured payload.
	RawAll RawPolicy = "all"
)

// DefaultRawPolicy is RawLossyOnly, per the IR3 design decision (2026-07):
// capture what would otherwise be silently lost; skip byte-for-byte
// duplication of content already captured structurally.
const DefaultRawPolicy = RawLossyOnly

// normalizeRawPolicy maps an empty/unrecognized policy string to the default,
// so a caller that leaves ChatRequest.TranscriptRawPolicy unset (every
// current caller) gets DefaultRawPolicy rather than an invalid/zero policy
// that would silently behave like RawOff.
func normalizeRawPolicy(p RawPolicy) RawPolicy {
	switch p {
	case RawOff, RawLossyOnly, RawAll:
		return p
	default:
		return DefaultRawPolicy
	}
}

func entryPayload(e *agent.SessionEntry) *EntryPayload {
	return &EntryPayload{
		Type:          string(e.Type),
		Content:       e.Content,
		ToolName:      e.ToolName,
		ToolInput:     e.ToolInput,
		ToolOutput:    e.ToolOutput,
		IsError:       e.IsError,
		Sidechain:     e.Sidechain,
		ToolCallID:    e.ToolCallID,
		ToolKind:      e.ToolKind,
		ToolLocations: toolLocationsPayload(e.ToolLocations),
		ToolContent:   toolContentPayload(e.ToolContent),
		ContentBlocks: contentBlocksPayload(e.ContentBlocks),
		SystemKind:    string(e.SystemKind),
		Plan:          planEntriesPayload(e.Plan),
	}
}

func toolLocationsPayload(locs []agent.ToolLocation) []ToolLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]ToolLocation, 0, len(locs))
	for _, l := range locs {
		out = append(out, ToolLocation{Path: l.Path, Line: l.Line})
	}
	return out
}

func contentBlocksPayload(blocks []agent.ContentBlock) []ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, ContentBlock{Kind: b.Kind, Text: b.Text, Raw: b.Raw})
	}
	return out
}

func toolContentPayload(content []agent.ToolContentBlock) []ToolContentBlock {
	if len(content) == 0 {
		return nil
	}
	out := make([]ToolContentBlock, 0, len(content))
	for _, c := range content {
		out = append(out, ToolContentBlock{
			Kind: c.Kind, Text: c.Text,
			DiffPath: c.DiffPath, DiffOldText: c.DiffOldText, DiffNewText: c.DiffNewText,
			TerminalID: c.TerminalID, Raw: c.Raw,
		})
	}
	return out
}

func planEntriesPayload(entries []agent.PlanEntry) []PlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]PlanEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, PlanEntry{Content: e.Content, Priority: e.Priority, Status: e.Status})
	}
	return out
}

func sessionPayload(s *agent.ChatSessionInfo) *SessionPayload {
	p := &SessionPayload{
		Resumable:      s.Resumable,
		Model:          s.Model,
		PermissionMode: s.PermissionMode,
		ContextWindow:  s.ContextWindow,
	}
	for _, m := range s.MCPServers {
		p.MCPServers = append(p.MCPServers, MCPStatus{Name: m.Name, Status: m.Status})
	}
	return p
}

func completePayload(m *agent.TurnMeta) *CompletePayload {
	return &CompletePayload{
		InputTokens:         m.InputTokens,
		OutputTokens:        m.OutputTokens,
		CacheReadTokens:     m.CacheReadTokens,
		CacheCreationTokens: m.CacheCreationTokens,
		ContextWindow:       m.ContextWindow,
		MaxOutputTokens:     m.MaxOutputTokens,
		CostUSD:             m.CostUSD,
		Model:               m.Model,
		StopReason:          m.StopReason,
		DurationMs:          m.DurationMs,
		NumTurns:            m.NumTurns,
	}
}

func permissionPayload(p *agent.PermissionRequest) *PermissionPayload {
	out := &PermissionPayload{
		ID:         p.ID,
		ToolName:   p.ToolName,
		ToolInput:  p.ToolInput,
		Kind:       p.Kind,
		ToolCallID: p.ToolCallID,
	}
	for _, o := range p.Options {
		out.Options = append(out.Options, PermissionOption{ID: o.ID, Kind: o.Kind, Name: o.Name})
	}
	return out
}
