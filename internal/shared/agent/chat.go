package agent

import (
	"context"
	"encoding/json"
)

// StructuredChat is an OPTIONAL backend capability: a persistent, multi-turn
// structured conversation over the backend's NATIVE programmatic protocol — not
// a pty/TUI. A backend implements it only if it can speak such a protocol; the
// host discovers support via a type assertion (backend.(StructuredChat)) and
// reports the feature unavailable otherwise. claude-code implements it over its
// `--input-format stream-json` mode; other backends may not yet.
//
// This is deliberately separate from the core Backend interface: adding a
// required method would break every backend, and structured chat is a capability
// some agents simply lack.
type StructuredChat interface {
	// Chat runs one conversation for the lifetime of the call.
	//
	// Contract:
	//   - The caller owns `in` and CLOSES it to signal "no more input".
	//   - The implementation produces on `out` and CLOSES `out` exactly once,
	//     before returning (producer owns the close).
	//   - Chat returns when `in` is closed and drained and the final response has
	//     completed, when ctx is cancelled (returning ctx.Err()), or on a fatal
	//     error. The caller ranges over `out` until it closes to consume events.
	//   - The implementation owns its subprocess for the call's duration and must
	//     not close anything the caller owns.
	Chat(ctx context.Context, req ChatRequest, in <-chan ChatMessage, out chan<- ChatEvent) error
}

// ChatRequest configures a structured chat run. Mirrors the subset of
// ExecuteRequest a programmatic (non-pty) conversation needs.
type ChatRequest struct {
	WorkDir     string
	Model       string
	Env         map[string]string
	AutoApprove bool
	// ForwardPermissions asks the backend to surface each engine permission
	// request as a ChatEvent.Permission and park the engine until the matching
	// ChatMessage.Permission answer arrives, instead of auto-deciding
	// (AutoApprove → allow, otherwise reject). Only meaningful to a caller that
	// can actually answer — an interactive host with a human behind it.
	ForwardPermissions bool
	// MCPServers are caller-supplied MCP servers to attach to the conversation
	// (e.g. the ACP client's session/new mcpServers), in addition to whatever
	// native config the engine reads from its cwd.
	MCPServers []ChatMCPServer
}

// ChatMCPServer is one caller-supplied stdio MCP server for a chat run.
type ChatMCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// ChatMessage is one inbound message on a chat's input channel. Exactly one
// field is meaningful: Text is a user turn; Permission answers a pending
// ChatEvent.Permission request; CancelTurn asks the backend to abandon the
// in-flight turn (the conversation stays alive and the turn completes with
// StopReason "cancelled"). Richer content blocks (e.g. images) are a later
// addition.
type ChatMessage struct {
	Text       string
	Permission *PermissionAnswer
	CancelTurn bool
}

// ChatEvent is one normalized outbound event. The variants are distinct in
// payload, cardinality, and timing — NOT duplicative; exactly one field is set:
//
//   - Entry      — conversation CONTENT, one atomic piece, MANY per response
//     (assistant text block, tool_use, tool_result). This is the turn's substance.
//   - Complete   — the response's COMPLETION marker, ONE after the entries, carrying
//     only accounting (tokens/context/cost/timing), NO content. Lets a client end
//     the turn (re-enable input) and update a context-window gauge.
//   - Session    — one-time session metadata, emitted once at the start.
//   - Permission — the engine is asking for authorization mid-turn (only under
//     ChatRequest.ForwardPermissions); the caller answers with a
//     ChatMessage.Permission carrying the same ID. The turn stays parked until
//     the answer arrives.
type ChatEvent struct {
	Entry      *SessionEntry
	Complete   *TurnMeta
	Session    *ChatSessionInfo
	Permission *PermissionRequest
}

// PermissionRequest is a forwarded engine permission request: the engine wants
// to run ToolName and offers the given decision options. ID correlates the
// eventual PermissionAnswer (unique within one chat).
type PermissionRequest struct {
	ID        string
	ToolName  string
	ToolInput json.RawMessage
	Options   []PermissionOption
}

// PermissionOption is one decision the engine offers for a permission request.
// Kind is the ACP option-kind vocabulary: allow_once | allow_always |
// reject_once | reject_always.
type PermissionOption struct {
	ID   string
	Kind string
	Name string
}

// PermissionAnswer resolves the PermissionRequest with the same ID: OptionID
// names the chosen option; empty means the request was dismissed/cancelled
// (the engine treats that as neither an approval nor a remembered rejection).
type PermissionAnswer struct {
	ID       string
	OptionID string
}

// TurnMeta is backend-agnostic completion metadata for one response: a client can
// surface a context-window gauge, cost, and timing. Backends fill what they can;
// a zero field means "unknown".
type TurnMeta struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	ContextWindow       int // model's context window (for an "x / N" gauge)
	MaxOutputTokens     int
	CostUSD             float64
	Model               string
	StopReason          string
	DurationMs          int
	NumTurns            int
}

// ChatSessionInfo is one-time metadata emitted at the start of a chat (kept
// distinct from SessionMeta, which is transcript-store metadata).
type ChatSessionInfo struct {
	Model          string
	PermissionMode string
	ContextWindow  int
	MCPServers     []MCPStatus
}

// MCPStatus is the connection status of one MCP server at session start.
type MCPStatus struct {
	Name   string
	Status string
}
