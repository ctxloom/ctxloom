package cli

import (
	"encoding/json"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// rfc3339OrEmpty formats a turn's timestamp for the NDJSON wire, returning ""
// for the zero time so the field is omitted (the frontend then falls back to its
// own clock). The backend stamps chat entries at receipt; transcript entries
// carry their own time.
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// --- NDJSON output DTOs (the structured-frontend contract; camelCase fields) ---

type chatEntryJSON struct {
	Type       string          `json:"type"`
	Timestamp  string          `json:"timestamp,omitempty"` // RFC3339; the frontend's protocol resolves it (or now())
	Content    string          `json:"content,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	ToolInput  json.RawMessage `json:"toolInput,omitempty"`
	ToolOutput string          `json:"toolOutput,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	// Sidechain marks an entry belonging to an engine's own in-harness
	// subagent rather than the session's main thread (agent.SessionEntry.
	// Sidechain). A frontend that does not attribute subagent-interior events
	// renders a subagent's whole conversation as if the user had had it.
	Sidechain bool `json:"sidechain,omitempty"`

	// --- IR2 (2026-07) richness. Mirrors agent.SessionEntry field for field;
	// all optional, a zero value meaning "the producing backend had none".
	// These were silently absent from this channel until the mirror-parity
	// gate (internal/adapters/cli/arch_test.go) was pointed at it.
	ToolCallID    string                 `json:"toolCallId,omitempty"`
	ToolKind      string                 `json:"toolKind,omitempty"`
	ToolLocations []chatToolLocationJSON `json:"toolLocations,omitempty"`
	ToolContent   []chatToolContentJSON  `json:"toolContent,omitempty"`
	ContentBlocks []chatContentBlockJSON `json:"contentBlocks,omitempty"`
	SystemKind    string                 `json:"systemKind,omitempty"`
	Plan          []chatPlanEntryJSON    `json:"plan,omitempty"`
}

// chatToolLocationJSON mirrors agent.ToolLocation.
type chatToolLocationJSON struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// chatContentBlockJSON mirrors agent.ContentBlock.
type chatContentBlockJSON struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

// chatToolContentJSON mirrors agent.ToolContentBlock.
type chatToolContentJSON struct {
	Kind        string          `json:"kind"`
	Text        string          `json:"text,omitempty"`
	DiffPath    string          `json:"diffPath,omitempty"`
	DiffOldText string          `json:"diffOldText,omitempty"`
	DiffNewText string          `json:"diffNewText,omitempty"`
	TerminalID  string          `json:"terminalId,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// chatPlanEntryJSON mirrors agent.PlanEntry.
type chatPlanEntryJSON struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

type chatCompleteJSON struct {
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	ContextWindow       int     `json:"contextWindow"`
	MaxOutputTokens     int     `json:"maxOutputTokens"`
	CostUSD             float64 `json:"costUsd"`
	Model               string  `json:"model,omitempty"`
	StopReason          string  `json:"stopReason,omitempty"`
	DurationMs          int     `json:"durationMs"`
	NumTurns            int     `json:"numTurns"`
}

type chatMCPJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type chatSessionJSON struct {
	// SessionID is the harness-NATIVE session id (agent.ChatSessionInfo.
	// SessionID) — the resume handle. Without it a frontend cannot offer
	// "continue this conversation" at all.
	SessionID string `json:"sessionId,omitempty"`
	// Resumable is the LIVE half of the resume gate: the connected adapter's
	// own handshake advertising session/load (agent.ChatSessionInfo.
	// Resumable), as distinct from the static per-backend capability table.
	Resumable      bool          `json:"resumable,omitempty"`
	Model          string        `json:"model,omitempty"`
	PermissionMode string        `json:"permissionMode,omitempty"`
	ContextWindow  int           `json:"contextWindow,omitempty"`
	MCPServers     []chatMCPJSON `json:"mcpServers,omitempty"`
}

// chatEventType* are the NDJSON contract's discriminator values. The frontend
// switches on this field, so they are a published vocabulary — named here so
// every producer of the feed is linked to it by the compiler rather than by
// two files happening to spell the same word.
const (
	chatEventTypeEntry    = "entry"
	chatEventTypeComplete = "complete"
	chatEventTypeSession  = "session"
)

type chatEventJSON struct {
	Type     string            `json:"type"` // chatEventType*
	Entry    *chatEntryJSON    `json:"entry,omitempty"`
	Complete *chatCompleteJSON `json:"complete,omitempty"`
	Session  *chatSessionJSON  `json:"session,omitempty"`
}

func chatEventToJSON(ev agent.ChatEvent) chatEventJSON {
	switch {
	case ev.Entry != nil:
		e := ev.Entry
		return chatEventJSON{Type: chatEventTypeEntry, Entry: &chatEntryJSON{
			Type:          string(e.Type),
			Timestamp:     rfc3339OrEmpty(e.Timestamp),
			Content:       e.Content,
			ToolName:      e.ToolName,
			ToolInput:     e.ToolInput,
			ToolOutput:    e.ToolOutput,
			IsError:       e.IsError,
			Sidechain:     e.Sidechain,
			ToolCallID:    e.ToolCallID,
			ToolKind:      e.ToolKind,
			ToolLocations: chatToolLocationsJSON(e.ToolLocations),
			ToolContent:   chatToolContentsJSON(e.ToolContent),
			ContentBlocks: chatContentBlocksJSON(e.ContentBlocks),
			SystemKind:    string(e.SystemKind),
			Plan:          chatPlanEntriesJSON(e.Plan),
		}}
	case ev.Complete != nil:
		c := ev.Complete
		return chatEventJSON{Type: chatEventTypeComplete, Complete: &chatCompleteJSON{
			InputTokens:         c.InputTokens,
			OutputTokens:        c.OutputTokens,
			CacheReadTokens:     c.CacheReadTokens,
			CacheCreationTokens: c.CacheCreationTokens,
			ContextWindow:       c.ContextWindow,
			MaxOutputTokens:     c.MaxOutputTokens,
			CostUSD:             c.CostUSD,
			Model:               c.Model,
			StopReason:          c.StopReason,
			DurationMs:          c.DurationMs,
			NumTurns:            c.NumTurns,
		}}
	case ev.Session != nil:
		s := ev.Session
		out := &chatSessionJSON{
			SessionID:      s.SessionID,
			Resumable:      s.Resumable,
			Model:          s.Model,
			PermissionMode: s.PermissionMode,
			ContextWindow:  s.ContextWindow,
		}
		for _, m := range s.MCPServers {
			out.MCPServers = append(out.MCPServers, chatMCPJSON{Name: m.Name, Status: m.Status})
		}
		return chatEventJSON{Type: chatEventTypeSession, Session: out}
	default:
		return chatEventJSON{}
	}
}

func chatToolLocationsJSON(locs []agent.ToolLocation) []chatToolLocationJSON {
	if len(locs) == 0 {
		return nil
	}
	out := make([]chatToolLocationJSON, 0, len(locs))
	for _, l := range locs {
		out = append(out, chatToolLocationJSON{Path: l.Path, Line: l.Line})
	}
	return out
}

func chatToolContentsJSON(blocks []agent.ToolContentBlock) []chatToolContentJSON {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]chatToolContentJSON, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, chatToolContentJSON{
			Kind:        b.Kind,
			Text:        b.Text,
			DiffPath:    b.DiffPath,
			DiffOldText: b.DiffOldText,
			DiffNewText: b.DiffNewText,
			TerminalID:  b.TerminalID,
			Raw:         b.Raw,
		})
	}
	return out
}

func chatContentBlocksJSON(blocks []agent.ContentBlock) []chatContentBlockJSON {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]chatContentBlockJSON, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, chatContentBlockJSON{Kind: b.Kind, Text: b.Text, Raw: b.Raw})
	}
	return out
}

func chatPlanEntriesJSON(entries []agent.PlanEntry) []chatPlanEntryJSON {
	if len(entries) == 0 {
		return nil
	}
	out := make([]chatPlanEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, chatPlanEntryJSON{Content: e.Content, Priority: e.Priority, Status: e.Status})
	}
	return out
}
