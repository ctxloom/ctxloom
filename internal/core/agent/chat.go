package agent

import (
	"encoding/json"
	"strconv"
)

// MCPTransport selects the wire-transport variant of one ChatMCPServer entry.
// The zero value (MCPTransportStdio, "") is the protocol's unconditional
// baseline — every EXISTING construction site (ComposeChatMCPServers and
// everything that feeds it: ctxloom's own bundle/config-managed servers,
// which are stdio-only today — see wire.MCPServer) leaves this field unset
// and is therefore completely unaffected by its addition. Http/Sse carry an
// EDITOR-supplied remote MCP server instead of a local command (ACP's
// session/new mcpServers, B3/gap G11): ctxloom's own materialized bundle
// servers never populate these, only an editor-passthrough path does.
type MCPTransport string

const (
	// MCPTransportStdio is the explicit zero value: a local command ctxloom
	// (or the editor) spawns as a subprocess. Every ACP agent MUST support
	// this transport per spec, so it carries no capability gate.
	MCPTransportStdio MCPTransport = ""
	// MCPTransportHTTP is a remote MCP server reached over streamable HTTP.
	// Only meaningful when the RECEIVING engine advertises
	// mcpCapabilities.http.
	MCPTransportHTTP MCPTransport = "http"
	// MCPTransportSSE is a remote MCP server reached over Server-Sent
	// Events. Only meaningful when the RECEIVING engine advertises
	// mcpCapabilities.sse.
	MCPTransportSSE MCPTransport = "sse"
)

// ChatMCPServer is one caller-supplied MCP server for a chat run: a stdio
// command (the default, Transport == MCPTransportStdio, Command/Args/Env
// meaningful) or a remote Http/Sse server (Transport set, URL/Headers
// meaningful, Command/Args/Env empty).
type ChatMCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string

	// Transport is MCPTransportStdio (default) for a local command, or
	// MCPTransportHTTP/MCPTransportSSE for a remote server.
	Transport MCPTransport
	// URL is the remote endpoint for an Http/Sse Transport entry. Empty for stdio.
	URL string
	// Headers are HTTP headers to send when connecting an Http/Sse Transport
	// entry (e.g. Authorization). Empty for stdio.
	Headers map[string]string
}

// Kind names the port-level kind of the event a driver relays
// (engine.Event.Kind): "session", "complete", the entry's own type, or
// "event" for a raw-only frame.
func (ev ChatEvent) Kind() string {
	switch {
	case ev.Session != nil:
		return "session"
	case ev.Complete != nil:
		return "complete"
	case ev.Denied != nil:
		return "denied"
	case ev.Entry != nil:
		return string(ev.Entry.Type)
	default:
		return "event"
	}
}

// ChatEvent is one normalized outbound event. The variants are distinct in
// payload, cardinality, and timing — NOT duplicative; exactly one field is set:
//
//   - Entry      — conversation CONTENT, one atomic piece, MANY per response
//     (assistant text block, tool_use, tool_result). This is the turn's substance.
//   - Complete   — the response's COMPLETION marker, ONE after the entries, carrying
//     only accounting (tokens/context/cost/timing) and the turn's denials, NO
//     content. Lets a client end the turn (re-enable input) and update a
//     context-window gauge.
//   - Session    — one-time session metadata, emitted once at the start.
//   - Denied     — a tool call the engine refused, as it happened. The turn's
//     completion carries every one again (TurnMeta.Denials): this variant is
//     the live signal, that one the turn's account.
type ChatEvent struct {
	Entry    *SessionEntry
	Complete *TurnMeta
	Session  *ChatSessionInfo
	Denied   *PermissionDenial

	// Raw is IR3's side channel: the ORIGINAL ACP session/update frame (or
	// just its `_meta` object), verbatim, for the CURATED ALLOWLIST of things
	// that have no IR projection of their own — today: available_commands_
	// update, current_mode_update, and any variant's `_meta` property (ACP's
	// vendor-extension escape hatch). It is what keeps those from being
	// SILENTLY LOST in transit (the conformance audit's gap G9): unlike
	// Entry/Complete/Session/Denied, Raw is not part of the "exactly one
	// set" union above — it may ride ALONGSIDE Entry (a `_meta` supplement to
	// an otherwise-fully-mapped entry) or stand ALONE (Entry/Complete/
	// Session/Denied all nil — a pure passthrough frame, e.g.
	// available_commands_update, that the IR has no other shape for at all).
	//
	// PERMISSIONS NEVER RIDE HERE. Never add a producer that marshals a
	// permission-shaped frame into Raw: mediation is exactly where ctxloom's
	// trust layer injects, and a byte tunnel would defeat it.
	//
	// Raw is NOT internal/adapters/transcript's Record.Raw (record.go). That is a
	// SEPARATE capture-layer field, populated FROM this one under the
	// transcript.raw capture policy (off | lossy-only | all, default
	// lossy-only) — a decision about what gets written to DISK, unrelated to
	// what crosses the WIRE. Do not conflate the two in code or docs; see
	// internal/adapters/transcript/recorder.go's RawPolicy doc comment.
	Raw json.RawMessage
}

// Decider names who decided a denial. The zero value is the engine's own
// policy — its posture and rules — which is every denial an engine reports
// on its own; the runner, which sees the approval route, names the others.
type Decider int

const (
	DeciderPolicy Decider = iota
	DeciderHuman
	DeciderTimeout
	DeciderCancelled
	DeciderRefused
	DeciderPlanPosture
	DeciderGrant
)

// String is the spelling the parent's blocked report carries.
func (d Decider) String() string {
	switch d {
	case DeciderPolicy:
		return "policy"
	case DeciderHuman:
		return "human"
	case DeciderTimeout:
		return "timeout"
	case DeciderCancelled:
		return "cancelled"
	case DeciderRefused:
		return "refused"
	case DeciderPlanPosture:
		return "plan posture"
	case DeciderGrant:
		return "grant"
	default:
		return "decider(" + strconv.Itoa(int(d)) + ")"
	}
}

// PermissionDenial is one tool call the engine did not run because nothing
// allowed it. Reason is the engine's own words, when it gave any.
type PermissionDenial struct {
	ToolName, ToolCallID, Reason string
	Decider                      Decider
}

// TurnMeta is backend-agnostic completion metadata, emitted once per response:
// a client can surface a context-window gauge, cost, and timing. Backends fill
// what they can; a zero field means "unknown".
//
// EMITTED per turn; MEASURED per SESSION. The token and cost fields report the
// conversation's running totals as of this turn, NOT that turn's own
// consumption — the field names say "this response" and the values do not, so
// the distinction is spelled out here rather than left to be rediscovered.
// It is what every reader already assumes: the gauge renders
// InputTokens/ContextWindow as "used out of the window", ACP's own
// usage_update is cumulative by specification (its `used` is "tokens currently
// in context", its `cost` "cumulative session cost") and round-trips through
// this struct, and agentcoord bills the LAST turn's meta as the session total.
// A backend that reports per-turn deltas here therefore under-reports the
// session everywhere at once, and silently.
//
// StopReason, DurationMs and Model are the per-turn exceptions, as their names
// suggest: each describes the one response this meta completes.
type TurnMeta struct {
	InputTokens         int // session total: tokens in context as of this turn
	OutputTokens        int // session total
	CacheReadTokens     int // session total
	CacheCreationTokens int // session total
	ContextWindow       int // model's context window (for an "x / N" gauge)
	MaxOutputTokens     int
	CostUSD             float64 // session total, in USD
	Model               string
	StopReason          string
	DurationMs          int
	NumTurns            int
	// Denials is every tool call the turn's engine refused. A turn with
	// any did not do all it was asked: it is BLOCKED, not done.
	Denials []PermissionDenial
}

// ChatSessionInfo is one-time metadata emitted at the start of a chat (kept
// distinct from SessionMeta, which is transcript-store metadata).
type ChatSessionInfo struct {
	// SessionID is the harness-NATIVE session id this conversation runs
	// under (the ACP session id from session/new or session/load) — the
	// resume handle a coordinator journals so a later respawn can continue
	// the same native session (engine.Turn.Resume). Empty when the
	// backend exposes none.
	SessionID string
	// Resumable reports that the backend advertised it can RESUME this native
	// session by its SessionID key on a later spawn (ACP: the engine's
	// initialize-time loadSession capability). It is
	// the LIVE half of the one-shot resume gate (one-shot-resume plan, Slice 4
	// / Fork 3): the static per-backend table says a backend COULD resume, but
	// only the connected adapter's own handshake proves THIS engine actually
	// advertises it — a mismatch (statically capable, live-unadvertised) must
	// fall back to the persistent warm-engine model rather than tear down at a
	// turn boundary and then fail loud at session/load. False when the backend
	// exposes no such capability (or has not reported one yet).
	Resumable      bool
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
