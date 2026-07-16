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
	Permissions PermissionMode
	// ForwardPermissions asks the backend to surface each engine permission
	// request as a ChatEvent.Permission and park the engine until the matching
	// ChatMessage.Permission answer arrives, instead of auto-deciding
	// (Permissions bypass → allow, otherwise reject). Only meaningful to a caller
	// that can actually answer — an interactive host with a human behind it.
	ForwardPermissions bool
	// MCPServers are caller-supplied MCP servers to attach to the conversation
	// (e.g. the ACP client's session/new mcpServers), in addition to whatever
	// native config the engine reads from its cwd.
	MCPServers []ChatMCPServer
	// ResumeSessionID, when set, asks the backend to resume a prior native
	// session instead of starting fresh (claude --resume <id>, codex
	// thread/resume, ACP session/load). A backend that cannot resume (no
	// native support, or the specific id is unknown to it) fails the call
	// loudly rather than silently starting a fresh session under the old
	// id's name — a delegated child's resumed context is load-bearing.
	ResumeSessionID string
	// TranscriptRawPolicy names the transcript.raw capture policy this chat's
	// canonical-transcript Recorder should honor (transcript.RawPolicy: off |
	// lossy-only | all — see internal/transcript/recorder.go). Empty means
	// "use the default" (lossy-only). This is a CAPTURE-layer setting riding
	// ChatRequest purely as a convenient existing carrier from host to the
	// point a Recorder gets constructed (internal/lm/grpc/chat.go,
	// internal/agentcoord/coord/enginehost.go) — it has nothing to do with
	// the chat itself and a backend implementation never reads it. NOTE:
	// nothing yet POPULATES this from user config (that CLI-boundary wiring
	// — reading config.Config's transcript.raw key at run_structured/oneshot/
	// acp call sites — is deferred); every current caller leaves it empty, so
	// every current transcript keeps recording under the default policy
	// exactly as before this field existed.
	TranscriptRawPolicy string
	// Runtime asks the backend to run the underlying engine SUBPROCESS inside
	// a container instead of directly on the host: RuntimeContainer
	// containerizes it, "" (or anything else) means host — today's behavior,
	// unchanged. It carries the AGENT BINDING's resolved runtime axis (see
	// isolation.RuntimeAxis / ResolvedAgent.Runtime) into a structured chat,
	// which the axis could not reach before (ISO1): this package sits below
	// internal/lm/isolation in the import graph (isolation -> lm/grpc ->
	// this package), so the axis rides as a bare string rather than an
	// imported type, to avoid a cycle back to here. Only a backend whose
	// StructuredChat transport actually implements container isolation (the
	// ACP client driver, internal/acp) consults it; every other backend
	// ignores it — additive, host stays the default everywhere else.
	Runtime string
}

// RuntimeContainer is the ChatRequest.Runtime value asking a StructuredChat
// backend to run its engine subprocess inside a container. Mirrors
// isolation.RuntimeContainer's string value byte-for-byte (see Runtime's doc
// for why this is a duplicated literal, not an import).
const RuntimeContainer = "container"

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
	// ContentBlocks carries structured content blocks for a richer turn than
	// plain Text (e.g. multiple blocks). Additive: a caller that only sends
	// Text need not change, and no backend consumes this yet — populating
	// non-text blocks (image/audio) is multimodal intake, a later slice
	// (B2). It exists now so the wire can carry them ahead of that slice.
	ContentBlocks []ContentBlock
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

	// Raw is IR3's side channel: the ORIGINAL ACP session/update frame (or
	// just its `_meta` object), verbatim, for the CURATED ALLOWLIST of things
	// that have no IR projection of their own — today: available_commands_
	// update, current_mode_update, and any variant's `_meta` property (ACP's
	// vendor-extension escape hatch). It is what keeps those from being
	// SILENTLY LOST in transit (the conformance audit's gap G9): unlike
	// Entry/Complete/Session/Permission, Raw is not part of the "exactly one
	// set" union above — it may ride ALONGSIDE Entry (a `_meta` supplement to
	// an otherwise-fully-mapped entry) or stand ALONE (Entry/Complete/
	// Session/Permission all nil — a pure passthrough frame, e.g.
	// available_commands_update, that the IR has no other shape for at all).
	// See internal/acp/mapping.go (producer) and internal/acpagent/mapping.go
	// (consumer/re-emitter) for the allowlist enforcement.
	//
	// PERMISSIONS NEVER RIDE HERE. session/request_permission is not even a
	// session/update variant (it is a separate agent→client REQUEST,
	// mediated end to end via Permission above), so it structurally cannot
	// reach Raw — this is not a filter that could be bypassed, permission
	// requests are never in the candidate set to begin with. Never add a
	// producer that marshals a permission-shaped frame into Raw: mediation is
	// exactly where ctxloom's trust layer injects, and a byte tunnel would
	// defeat it.
	//
	// Raw is NOT internal/transcript's Record.Raw (record.go). That is a
	// SEPARATE capture-layer field, populated FROM this one under the
	// transcript.raw capture policy (off | lossy-only | all, default
	// lossy-only) — a decision about what gets written to DISK, unrelated to
	// what crosses the WIRE. Do not conflate the two in code or docs; see
	// internal/transcript/recorder.go's RawPolicy doc comment.
	Raw json.RawMessage
}

// PermissionRequest is a forwarded engine permission request: the engine wants
// to run ToolName and offers the given decision options. ID correlates the
// eventual PermissionAnswer (unique within one chat).
type PermissionRequest struct {
	ID        string
	ToolName  string
	ToolInput json.RawMessage
	Options   []PermissionOption
	// Kind is the connector-classified tool category, when the backend's
	// native protocol supplies one (ACP's ToolCallKind: "execute" | "edit" |
	// "delete" | "move" | "read" | "search" | "fetch" | "think" | "other").
	// Empty means unclassified. Purely advisory metadata carried through to
	// whatever buckets the request under a policy (e.g. the agentcoord
	// escalation ladder's ApprovalKind, Wave C2) — backends that cannot
	// classify simply leave it empty.
	Kind string
	// ToolCallID is the engine-native tool-call id this permission request
	// refers to, when the backend's protocol supplies one (ACP's
	// RequestPermissionRequest.toolCall.toolCallId). Carried through so a
	// re-emission can target the SAME id instead of guessing one by tool
	// name — the same fix as SessionEntry.ToolCallID, applied to the
	// permission-forwarding path. Empty means unknown; a consumer falls back
	// to its own name-based lookup exactly as before this field existed.
	ToolCallID string
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
	// SessionID is the harness-NATIVE session id this conversation runs
	// under (the ACP session id from session/new or session/load) — the
	// resume handle a coordinator journals so a later respawn can continue
	// the same native session (ChatRequest.ResumeSessionID). Empty when the
	// backend exposes none.
	SessionID      string
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
