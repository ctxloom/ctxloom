// Package transcript owns ctxloom's OWN captured conversation record — the
// canonical transcript.acp.jsonl file under a harp's persist/ dir
// (paths.HarpCanonicalTranscriptPath). It is the runner-side alternative to
// scraping each engine's private, version-unstable session-store file (see
// docs/transcript-schema.md for the full design rationale, "tough-cloud").
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
)

// Record is one line of the canonical transcript.acp.jsonl file.
type Record struct {
	// V is the schema version this line was written under (SchemaVersion at
	// write time). Never omitted — an absent v is itself a format error.
	V int `json:"v"`
	// Harp is the ctxloom session id — the authoritative key. Every line in a
	// given transcript.acp.jsonl file carries the same harp (the file lives
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
	// Engine names the backend driving this conversation: codex|kiro|claude|
	// opencode|acp|antigravity. antigravity never reaches this recorder via
	// the structured tee (it has no StructuredChat capability today) — its
	// canonical lines, when they exist, come from the oneshot/importer
	// regimes (plan §2d), which stamp Engine the same way.
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

	// Raw is an OPTIONAL escape hatch for the original engine frame, reserved
	// for a capture path that holds one and finds the ChatEvent mapping lossy
	// for a given frame (see docs/transcript-schema.md, "Fidelity"). The
	// ChatEvent-driven Recorder in THIS package never populates it — ChatEvent
	// carries no raw frame to attach. It exists in the schema now so S6
	// importers (which DO hold the native engine frame) and any future
	// richer capture path can use the same envelope without a schema bump.
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
}

// SessionPayload is the KindSession payload — agent.ChatSessionInfo minus
// SessionID (hoisted to the envelope).
type SessionPayload struct {
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
// (Recorder.Record) to stamp. Returns an error for a zero-value ChatEvent (no
// variant set) — the caller must not silently emit a line with no payload; a
// blank envelope masquerading as a recorded event is exactly the
// silent-no-op failure mode this package exists to avoid.
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
	default:
		return "", nil, nil, nil, nil, fmt.Errorf("transcript: ChatEvent carries no variant (Entry/Session/Complete/Permission all nil) — refusing to record an empty line")
	}
}

func entryPayload(e *agent.SessionEntry) *EntryPayload {
	return &EntryPayload{
		Type:       string(e.Type),
		Content:    e.Content,
		ToolName:   e.ToolName,
		ToolInput:  e.ToolInput,
		ToolOutput: e.ToolOutput,
		IsError:    e.IsError,
		Sidechain:  e.Sidechain,
	}
}

func sessionPayload(s *agent.ChatSessionInfo) *SessionPayload {
	p := &SessionPayload{
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
		ID:        p.ID,
		ToolName:  p.ToolName,
		ToolInput: p.ToolInput,
		Kind:      p.Kind,
	}
	for _, o := range p.Options {
		out.Options = append(out.Options, PermissionOption{ID: o.ID, Kind: o.Kind, Name: o.Name})
	}
	return out
}

// chatEventSessionID extracts the native session id carried by a Session
// event, when present — the value the recorder latches onto Record.SessionID
// for this and every subsequent line (see Record.SessionID doc).
func chatEventSessionID(ev agent.ChatEvent) string {
	if ev.Session != nil {
		return ev.Session.SessionID
	}
	return ""
}
