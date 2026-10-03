package claude

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// This file maps claude-code's `--output-format stream-json` events to ctxloom's
// backend-agnostic chat turns. All claude-specific wire knowledge lives here (per
// the polymorphic design): shared/agent and ctxloom only ever see agent.ChatEvent.

// --- stream-json wire shapes (subset we consume) ---

type sjEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// Message is an object on assistant/user frames and a string on
	// system/permission_denied, so it is decoded per frame type (message,
	// deniedMessage) — a typed field would fail the whole frame on the other
	// shape, and a dropped frame is a dropped denial.
	Message json.RawMessage `json:"message"`
	// result fields
	Usage             *sjUsage              `json:"usage"`
	ModelUsage        map[string]sjModelUse `json:"modelUsage"`
	TotalCost         float64               `json:"total_cost_usd"`
	DurationMs        int                   `json:"duration_ms"`
	NumTurns          int                   `json:"num_turns"`
	StopReason        string                `json:"stop_reason"`
	PermissionDenials []sjDenial            `json:"permission_denials"`
	// system/init fields
	SessionID      string  `json:"session_id"`
	Model          string  `json:"model"`
	PermissionMode string  `json:"permissionMode"`
	MCPServers     []sjMCP `json:"mcp_servers"`
	// system/permission_denied fields
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	// assistant fields: the API error that ended the turn (claude's
	// SDKAssistantMessageError) and, for a sub-agent's message, the tool call
	// it runs under (null on the turn's own messages).
	Error           string  `json:"error"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// rate_limit_event fields (SDKRateLimitEvent)
	RateLimitInfo *sjRateLimitInfo `json:"rate_limit_info"`
}

// sjRateLimitInfo is a rate_limit_event's rate_limit_info. resetsAt is unix
// seconds (the captured rate_limit_event.json); documented as a number, so it
// is decoded as one — an integer field would drop the whole frame on a
// fractional value.
type sjRateLimitInfo struct {
	Status   string  `json:"status"`
	ResetsAt float64 `json:"resetsAt"`
}

// rejectedResetsAt is the reset time a rate_limit_event names for a REJECTED
// request; ok is false for any other frame, an allowed request, or no time.
func (e *sjEvent) rejectedResetsAt() (at time.Time, ok bool) {
	if e.Type != "rate_limit_event" || e.RateLimitInfo == nil ||
		e.RateLimitInfo.Status != "rejected" || e.RateLimitInfo.ResetsAt <= 0 {
		return time.Time{}, false
	}
	sec, frac := math.Modf(e.RateLimitInfo.ResetsAt)
	return time.Unix(int64(sec), int64(frac*1e9)), true
}

// turnFailures is the ONE declaration of which of claude's documented
// SDKAssistantMessageError values fail the turn, and as what.
//   - authentication_failed / oauth_org_not_allowed: the two claude's own SDK
//     host reads as an auth failure. account_on_hold is not one (signing in
//     again does not lift it), nor is cloud_credential_error (claude reports
//     a briefly unreachable credential service the same way).
//   - rate_limit: "a 429 against your quota", which outlasted claude's own
//     retries.
//   - overloaded: "a 529 because the server is at capacity", which outlasted
//     claude's own retries — its own kind, not a rate limit: it is no reason
//     to park the runs sharing the credential.
var turnFailures = map[string]agent.FailureKind{
	"authentication_failed": agent.FailureCredentialRejected,
	"oauth_org_not_allowed": agent.FailureCredentialRejected,
	"rate_limit":            agent.FailureRateLimited,
	"overloaded":            agent.FailureOverloaded,
}

// failure is the turn failure an assistant frame reports: only the turn's own
// message (no parent tool call) speaks for the turn.
func (e *sjEvent) failure() []agent.ChatEvent {
	kind, ok := turnFailures[e.Error]
	if e.ParentToolUseID != nil || !ok {
		return nil
	}
	return []agent.ChatEvent{{Failed: &agent.TurnFailure{Kind: kind}}}
}

// turnStream maps one turn's stream-json frames. Each frame maps on its own,
// with ONE join across frames: the reset time of a rejected rate_limit_event
// rides the turn's rate-limit failure, in whichever order claude sends them —
// the event names when the limit lifts, but only the turn's own error says the
// turn ended on it (claude retries temporary 429s itself). One per turn
// process, so nothing carries from one turn to the next.
type turnStream struct {
	resetsAt time.Time
	limited  bool
}

// mapLine normalizes one stream-json line into 0..N ChatEvents. An assistant
// message holds an array of content blocks, so one event can yield several
// entries. Unknown/irrelevant events (hook_*, thinking_tokens, an allowed
// rate_limit_event, malformed JSON, a future event type) return nil — the
// stream must never crash on something we don't model.
func (s *turnStream) mapLine(raw []byte) []agent.ChatEvent {
	var e sjEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	if at, ok := e.rejectedResetsAt(); ok {
		s.resetsAt = at
		if s.limited {
			return []agent.ChatEvent{{Failed: &agent.TurnFailure{Kind: agent.FailureRateLimited, ResetsAt: at}}}
		}
		return nil
	}
	evs := e.events()
	for _, ev := range evs {
		if ev.Failed != nil && ev.Failed.Kind == agent.FailureRateLimited {
			s.limited = true
			ev.Failed.ResetsAt = s.resetsAt
		}
	}
	return evs
}

// message is an assistant/user frame's message object; nil when absent or
// not an object.
func (e *sjEvent) message() *sjMessage {
	var m sjMessage
	if len(e.Message) == 0 || json.Unmarshal(e.Message, &m) != nil {
		return nil
	}
	return &m
}

// deniedMessage is a permission_denied frame's reason; "" when absent or not
// a string.
func (e *sjEvent) deniedMessage() string {
	var s string
	_ = json.Unmarshal(e.Message, &s)
	return s
}

// sjDenial is one result.permission_denials entry. claude also sends the
// call's tool_input; a denial is reported by tool and call, not replayed, so
// it is not read.
type sjDenial struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

type sjMessage struct {
	Content json.RawMessage `json:"content"` // string OR array of blocks
}

// claudeBlock is the content-block shape (text/thinking/tool_use/tool_result)
// claude-code emits both on this live stream-json transport and in its
// persisted transcript lines. This package now has exactly one consumer of it
// (this file) — the transcript-side twin (capabilities.go's parser) was
// deleted outright in tough-cloud S5 (2026, "session history and recovery
// deleted, not demoted"), so there is no second copy to keep in lockstep with
// any more.
type claudeBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"` // thinking block: reasoning prose (not in Text)
	Name     string          `json:"name"`
	ID       string          `json:"id"`          // tool_use: the call's id
	ToolUse  string          `json:"tool_use_id"` // tool_result: the call it answers
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
}

// claudeBlockText flattens a tool_result message's content (a bare string, or
// an array of blocks) down to its text.
func claudeBlockText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	if json.Unmarshal(content, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

type sjUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type sjModelUse struct {
	OutputTokens    int     `json:"outputTokens"`
	ContextWindow   int     `json:"contextWindow"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
	CostUSD         float64 `json:"costUSD"`
}

type sjMCP struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// events maps one decoded frame on its own.
func (e *sjEvent) events() []agent.ChatEvent {
	// e.Type compares against agent.SessionEntryType's own exported constants
	// (converted to string, since sjEvent.Type is a bare wire string) rather
	// than re-spelling "assistant"/"user"/"system" as literals — those three
	// wire values happen to coincide with ctxloom's internal vocabulary today.
	// "result" has no SessionEntryType member (it is TurnMeta, not an entry)
	// and stays a literal on purpose.
	switch e.Type {
	case string(agent.EntryTypeAssistant):
		return append(mapAssistantBlocks(e.message()), e.failure()...)
	case string(agent.EntryTypeUser):
		return mapToolResults(e.message())
	case "result":
		return []agent.ChatEvent{{Complete: resultToTurnMeta(e)}}
	case string(agent.EntryTypeSystem):
		switch e.Subtype {
		case "init":
			return []agent.ChatEvent{{Session: initToSessionInfo(e)}}
		case "permission_denied":
			// Engine-decided: the posture and rules refused the call, so the
			// decider is the engine's policy until the runner joins it with
			// an approval it saw.
			return []agent.ChatEvent{{Denied: &agent.PermissionDenial{ToolName: e.ToolName, ToolCallID: e.ToolUseID, Reason: e.deniedMessage(), Decider: agent.DeciderPolicy}}}
		}
		return nil
	default:
		return nil
	}
}

// mapAssistantBlocks turns an assistant message's content blocks into entries:
// text → assistant, thinking → thinking, tool_use → tool_use; other blocks are
// dropped.
func mapAssistantBlocks(m *sjMessage) []agent.ChatEvent {
	if m == nil {
		return nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		// content may be a bare string (rare for assistant).
		var s string
		if json.Unmarshal(m.Content, &s) == nil && s != "" {
			return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: s}}}
		}
		return nil
	}
	// b.Type compares against agent.SessionEntryType's exported constants
	// (converted to string) for the two wire values that coincide with the
	// internal vocabulary; "text" has no SessionEntryType member of its own
	// (a text block maps onto EntryTypeAssistant, not a same-named entry
	// type) and stays a literal.
	var out []agent.ChatEvent
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: b.Text}})
			}
		case string(agent.EntryTypeThinking):
			// Emit a thinking marker even when the text is blank. NOTE: claude-code
			// intentionally strips the reasoning text from its `-p --output-format
			// stream-json` output — the block arrives as {type:"thinking",
			// thinking:"", signature:"…"}, signature only, and no thinking_delta
			// events are emitted even with --include-partial-messages. The signature
			// is kept for multi-turn API replay; the prose is withheld from
			// programmatic consumers by design (the TUI shows it ephemerally instead).
			// So b.Thinking is empty in practice, but we still surface the block as a
			// content-less entry so a live frontend can show that the model reasoned
			// this turn. There are no timestamps in stream-json — only the turn-level
			// timing carried by the result/Complete event. Content carries the prose
			// unchanged if a future build (or a direct-API backend) ever provides it.
			out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: b.Thinking}})
		case string(agent.EntryTypeToolUse):
			out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:       agent.EntryTypeToolUse,
				ToolName:   b.Name,
				ToolCallID: b.ID,
				ToolInput:  json.RawMessage(b.Input),
			}})
		}
	}
	return out
}

// mapToolResults turns a user message's tool_result blocks into tool_result
// entries. The block carries tool_use_id (ToolCallID) but not the tool name,
// so ToolName is left empty.
func mapToolResults(m *sjMessage) []agent.ChatEvent {
	if m == nil {
		return nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil
	}
	var out []agent.ChatEvent
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:       agent.EntryTypeToolResult,
			ToolCallID: b.ToolUse,
			ToolOutput: claudeBlockText(b.Content),
			IsError:    b.IsError,
		}})
	}
	return out
}

// resultToTurnMeta extracts completion accounting. Token counts come from the
// turn-level usage; context-window / max-output / per-model limits come from the
// generating model's modelUsage entry. The `result` string is deliberately NOT
// read — content comes only from the assistant entry, never duplicated here.
func resultToTurnMeta(e *sjEvent) *agent.TurnMeta {
	tm := &agent.TurnMeta{
		CostUSD:    e.TotalCost,
		StopReason: e.StopReason,
		DurationMs: e.DurationMs,
		NumTurns:   e.NumTurns,
	}
	for _, d := range e.PermissionDenials {
		tm.Denials = append(tm.Denials, agent.PermissionDenial{ToolName: d.ToolName, ToolCallID: d.ToolUseID, Decider: agent.DeciderPolicy})
	}
	if e.Usage != nil {
		tm.InputTokens = e.Usage.InputTokens
		tm.OutputTokens = e.Usage.OutputTokens
		tm.CacheReadTokens = e.Usage.CacheReadInputTokens
		tm.CacheCreationTokens = e.Usage.CacheCreationInputTokens
	}
	model, mu := pickGeneratingModel(e.ModelUsage)
	tm.Model = model
	tm.ContextWindow = mu.ContextWindow
	tm.MaxOutputTokens = mu.MaxOutputTokens
	return tm
}

// pickGeneratingModel chooses the model that produced the result — the
// modelUsage entry with the most output tokens (ties broken on sorted id for
// determinism), matching the provenance rule used elsewhere in this backend.
func pickGeneratingModel(m map[string]sjModelUse) (string, sjModelUse) {
	return pickByMaxOutput(m, func(u sjModelUse) int { return u.OutputTokens })
}

// pickByMaxOutput returns the key of m whose out(value) is greatest, breaking
// ties on the lexicographically smallest key so the choice is deterministic; the
// zero key ("") is returned for an empty map. This is the single provenance rule
// both result parsers use to name the generating model: the CLI may route a large
// read through an ancillary fast model (high input, tiny output) while the
// requested model does the real generation, so output — not input — marks the
// working model. pickGeneratingModel (stream-json modelUsage) builds on it.
// The JSON-envelope parser's own copy of this rule (maxOutputModel) was
// deleted with claude's minimal form; this is the one remaining site, not a
// twin left behind.
// reprise:accept-drift
func pickByMaxOutput[T any](m map[string]T, out func(T) int) (string, T) {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var best string
	var bestVal T
	bestTokens := -1
	for _, id := range ids {
		if n := out(m[id]); n > bestTokens {
			best, bestVal, bestTokens = id, m[id], n
		}
	}
	return best, bestVal
}

func initToSessionInfo(e *sjEvent) *agent.ChatSessionInfo {
	// session_id is the native key the NEXT turn's process resumes by
	// (--resume): with one process per turn it is the continuity of the
	// session, and a key the driver reports is what the runner resumes with.
	s := &agent.ChatSessionInfo{Model: e.Model, PermissionMode: e.PermissionMode, SessionID: e.SessionID, Resumable: e.SessionID != ""}
	for _, m := range e.MCPServers {
		s.MCPServers = append(s.MCPServers, agent.MCPStatus{Name: m.Name, Status: m.Status})
	}
	return s
}
