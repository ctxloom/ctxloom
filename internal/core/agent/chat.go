package agent

import (
	"encoding/json"
)

// ChatRequest configures a structured chat run. Mirrors the subset of
// ExecuteRequest a programmatic (non-pty) conversation needs.
type ChatRequest struct {
	WorkDir     string
	Model       string
	Env         map[string]string
	Permissions PermissionMode
	// MCPServers are caller-supplied MCP servers to attach to the conversation
	// (e.g. the ACP client's session/new mcpServers), in addition to whatever
	// native config the engine reads from its cwd.
	MCPServers []ChatMCPServer
	// MCPConfigPath is the .mcp.json the runner delivered under the session
	// home naming exactly MCPServers; an engine whose argv takes a config
	// file names this path rather than writing its own. "" when the set is
	// empty.
	MCPConfigPath string
	// ResumeSessionID, when set, asks the backend to resume a prior native
	// session instead of starting fresh (claude --resume <id>, codex
	// thread/resume, ACP session/load). A backend that cannot resume (no
	// native support, or the specific id is unknown to it) fails the call
	// loudly rather than silently starting a fresh session under the old
	// id's name — a delegated child's resumed context is load-bearing.
	ResumeSessionID string
	// TranscriptRawPolicy names the transcript.raw capture policy this chat's
	// canonical-transcript Recorder should honor (transcript.RawPolicy: off |
	// lossy-only | all — see internal/adapters/transcript/recorder.go). Empty means
	// "use the default" (lossy-only). This is a CAPTURE-layer setting riding
	// ChatRequest purely as a convenient existing carrier from host to the
	// point a Recorder gets constructed (internal/lm/grpc/chat.go,
	// internal/core/coord/enginehost.go) — it has nothing to do with
	// the chat itself and a backend implementation never reads it. NOTE:
	// nothing yet POPULATES this from user config (that CLI-boundary wiring
	// — reading config.Config's transcript.raw key at run_structured/oneshot/
	// acp call sites — is deferred); every current caller leaves it empty, so
	// every current transcript keeps recording under the default policy
	// exactly as before this field existed.
	TranscriptRawPolicy string
	// Runtime is the AGENT BINDING's resolved runtime axis, as its spelling
	// (launch.RuntimeAxis's vocabulary; parsed once where the binding is
	// resolved). Empty is the host. It rides a structured chat to the one
	// backend whose transport containerizes the engine subprocess; every
	// other backend ignores it. A string here because the gRPC crossing
	// carries it as one and this package sits below the axis vocabulary.
	Runtime string
	// ModelQuirk optionally names a per-engine escape hatch (see
	// ModelDeliveryQuirk) that forces Model onto the session via a non-spec
	// call the structured-chat driver makes right after setup,
	// before the first prompt. nil — every backend today — means
	// no such call: the spec-standard delivery (--model / an env var / a
	// future session/set_config_option) is trusted to work.
	ModelQuirk *ModelDeliveryQuirk
}

// ModelDeliveryQuirk names a single, VERSION-SCOPED per-engine model-delivery
// defect that a structured-chat driver routes around with a non-spec call,
// instead of trusting the spec-standard channel every other engine uses. It
// exists ONLY because CO1's controlled experiment proved claude-code-acp
// 0.16.2 silently ignores every spec-standard model channel (argv, env, and
// it does not implement session/set_config_option at all — zero hits in its
// dist/*.js). This type is deliberately backend-neutral (it lives alongside
// ChatRequest, not inside any one backend) so the driver that executes it
// never needs to
// know which engine it is talking to — it just compares the connected
// agent's self-reported identity against these fields.
type ModelDeliveryQuirk struct {
	// Method is the non-spec JSON-RPC method to call with
	// {sessionId, modelId: <the requested model>}.
	Method string
	// AgentName/AdapterVersions restrict the call to the connected agent's
	// self-reported initialize agentInfo.name and an EXACT agentInfo.version
	// match — an unlisted version (including a hoped-for future fix that
	// finally speaks session/set_config_option) is left on the spec-standard
	// path untouched.
	AgentName       string
	AdapterVersions []string
}

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
//     only accounting (tokens/context/cost/timing), NO content. Lets a client end
//     the turn (re-enable input) and update a context-window gauge.
//   - Session    — one-time session metadata, emitted once at the start.
//   - Permission — an engine permission request, recorded to the transcript
//     (transcript.KindPermission). Nothing answers it: the launch's
//     permission mode settles it.
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
	// Raw is NOT internal/adapters/transcript's Record.Raw (record.go). That is a
	// SEPARATE capture-layer field, populated FROM this one under the
	// transcript.raw capture policy (off | lossy-only | all, default
	// lossy-only) — a decision about what gets written to DISK, unrelated to
	// what crosses the WIRE. Do not conflate the two in code or docs; see
	// internal/adapters/transcript/recorder.go's RawPolicy doc comment.
	Raw json.RawMessage
}

// PermissionRequest is an engine permission request as the transcript records
// it: the engine wants to run ToolName and offers the given decision options.
// ID is unique within one conversation.
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
}

// ChatSessionInfo is one-time metadata emitted at the start of a chat (kept
// distinct from SessionMeta, which is transcript-store metadata).
type ChatSessionInfo struct {
	// SessionID is the harness-NATIVE session id this conversation runs
	// under (the ACP session id from session/new or session/load) — the
	// resume handle a coordinator journals so a later respawn can continue
	// the same native session (ChatRequest.ResumeSessionID). Empty when the
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
