package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
	// ForwardTerminal asks the backend to surface each engine terminal/*
	// request (terminal/create, terminal/output, terminal/wait_for_exit,
	// terminal/kill, terminal/release) as a ChatEvent.Terminal and park the
	// engine until the matching ChatMessage.Terminal answer arrives (B1, gap
	// G6) — the exact same carrier shape as ForwardPermissions, applied to a
	// different upstream callback. Unlike ForwardPermissions (always
	// answerable — session/request_permission is a core, ungated ACP method),
	// this is only honest when the caller actually has a live upstream editor
	// that ADVERTISED the terminal capability at ITS OWN initialize: a
	// backend must NEVER advertise ClientCapabilities.Terminal: true to the
	// engine unless this is true AND actually wired (see
	// internal/acp/session.go's setup) — ctxloom brokers terminal/* to a real
	// editor, it never implements a terminal of its own. The one populator
	// (internal/acpagent/server.go, via internal/operations.OpenRequest.
	// ForwardTerminal) sets this from the connected editor's own
	// clientCapabilities.terminal; every other caller (delegated child agents
	// with no ACP editor upstream, e.g. agentcoord's HarnessSpec) leaves it
	// false, which is exactly correct: there is nothing to broker to.
	ForwardTerminal bool
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
	// ModelQuirk optionally names a per-engine escape hatch (see
	// ModelDeliveryQuirk) that forces Model onto the session via a non-spec
	// call the ACP driver (internal/acp/session.go) makes right after setup,
	// before the first prompt. nil — every backend but claude today — means
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
// dist/*.js) — see internal/claude/chat.go for the full defect citation, the
// one populator, and the removal condition. This type is deliberately
// backend-neutral (it lives alongside ChatRequest, not inside internal/acp)
// so the driver that executes it (internal/acp/session.go) never needs to
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

// RuntimeContainer is the ChatRequest.Runtime value asking a StructuredChat
// backend to run its engine subprocess inside a container. Mirrors
// isolation.RuntimeContainer's string value byte-for-byte (see Runtime's doc
// for why this is a duplicated literal, not an import).
const RuntimeContainer = "container"

// ACPTransportKind names how a backend's StructuredChat implementation
// actually reaches an ACP-speaking process: the engine's OWN CLI speaks ACP
// (ACPNative — kiro's `kiro-cli acp`, opencode's `opencode acp`, the generic
// "acp" backend's configured passthrough command), a SEPARATE third-party
// adapter binary wraps a CLI that has no ACP mode of its own (ACPAdapter —
// claude-code-acp wrapping `claude`, codex-acp wrapping `codex`), or the
// backend speaks no ACP at all and drives its own bespoke protocol instead
// (ACPBespoke — no current registrant; reserved for an engine whose CLI has
// neither a native ACP mode nor a third-party adapter). This is the single
// per-engine declaration every ACP-transport consumer (a Chat() PATH gate, a
// doctor/init readiness probe, an image-build install fragment) reads instead
// of re-deriving "is this an adapter engine" from a hardcoded name switch.
type ACPTransportKind int

const (
	// ACPNative is the zero value: the engine's own client binary speaks ACP
	// directly, so there is no separate adapter to install or probe.
	ACPNative ACPTransportKind = iota
	// ACPAdapter means a separate, PATH-resolved adapter binary (Binary)
	// wraps the engine's CLI to speak ACP on its behalf.
	ACPAdapter
	// ACPBespoke means the backend implements StructuredChat over its own
	// non-ACP protocol (prose, JSON, whatever the vendor CLI offers) — there
	// is no adapter to install and no native ACP mode to probe.
	ACPBespoke
)

// ACPTransport is one engine's complete ACP-transport declaration: how its
// structured chat reaches the model, and — for an ACPAdapter engine — enough
// provenance for a human or an init agent to vet the adapter before it's
// installed. Every field but Kind is meaningful only for ACPAdapter; a
// native or bespoke engine leaves them empty.
type ACPTransport struct {
	Kind ACPTransportKind
	// Binary is the adapter binary this engine's Chat() gate LookPath()s and
	// spawns (ACPAdapter only), e.g. "claude-code-acp".
	Binary string
	// InstallCmd is the exact command a user runs to install Binary
	// (ACPAdapter only) — the SINGLE source for what was previously a
	// hardcoded string duplicated across each engine's Chat() error text and
	// the doctor/init readiness warn.
	InstallCmd string
	// Publisher is who publishes the adapter (ACPAdapter only) — provenance
	// for vetting before a user or an init agent installs a third-party
	// binary onto PATH.
	Publisher string
	// SourceRepo is the URL an init agent or human reviews the adapter's
	// source at (ACPAdapter only). Empty when no repo URL is derivable from
	// this codebase's own record of the adapter (never fabricate one).
	SourceRepo string
}

// RequireOnHost is the ONE gate every ACPAdapter engine's Chat() runs before
// spawning its adapter: for a HOST-runtime chat (runtime != RuntimeContainer)
// it LookPath()s Binary and fails loud, naming InstallCmd, if it's absent —
// a runtime:container chat is EXEMPT (the agent image carries its own
// adapter; this process's PATH is irrelevant there — see
// internal/lm/isolation's container-runtime axis). A native or bespoke
// transport (Kind != ACPAdapter) always reports nil: there is nothing on
// PATH for those to look up. engineLabel names the engine in the error text
// (e.g. "claude", "codex") — this method carries no opinion about which
// engine it's gating, only what one ACPAdapter declaration requires.
//
// Centralizing this (rather than each ACPAdapter engine's Chat() re-writing
// the same LookPath-or-fail-with-InstallCmd shape) is what makes the gate
// itself, not just the underlying binary/InstallCmd data, a single
// declaration every ACPAdapter engine shares.
func (t ACPTransport) RequireOnHost(runtime, engineLabel string) error {
	if runtime == RuntimeContainer || t.Kind != ACPAdapter {
		return nil
	}
	if _, err := exec.LookPath(t.Binary); err != nil {
		return fmt.Errorf("structured chat for %s needs the %s adapter on PATH; install it with: %s", engineLabel, t.Binary, t.InstallCmd)
	}
	return nil
}

// MCPTransport selects the wire-transport variant of one ChatMCPServer entry.
// The zero value (MCPTransportStdio, "") is the protocol's unconditional
// baseline — every EXISTING construction site (ComposeChatMCPServers and
// everything that feeds it: ctxloom's own bundle/config-managed servers,
// which are stdio-only today — see wire.MCPServer) leaves this field unset
// and is therefore completely unaffected by its addition. Http/Sse carry an
// EDITOR-supplied remote MCP server instead of a local command (ACP's
// session/new mcpServers, B3/gap G11): ctxloom's own materialized bundle
// servers never populate these, only the ACP passthrough paths
// (mcpServersFromACP / mcpServersToACP) do.
type MCPTransport string

const (
	// MCPTransportStdio is the explicit zero value: a local command ctxloom
	// (or the editor) spawns as a subprocess. Every ACP agent MUST support
	// this transport per spec, so it carries no capability gate.
	MCPTransportStdio MCPTransport = ""
	// MCPTransportHTTP is a remote MCP server reached over streamable HTTP.
	// Only meaningful when the RECEIVING engine advertises
	// mcpCapabilities.http — see internal/acp/session.go's mcpServersToACP.
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
	// Terminal answers a pending ChatEvent.Terminal request (B1, gap G6) —
	// the upstream editor's reply to one brokered terminal/* call, keyed by
	// the same ID. Mirrors Permission's carrier shape exactly.
	Terminal *TerminalResponse
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
//   - Terminal    — the engine is asking to broker one terminal/* operation to
//     the upstream editor (only under ChatRequest.ForwardTerminal, B1 gap G6);
//     the caller answers with a ChatMessage.Terminal carrying the same ID.
//     Same shape and parking discipline as Permission, applied to a different
//     upstream callback — ctxloom never implements a terminal of its own, it
//     only relays.
type ChatEvent struct {
	Entry      *SessionEntry
	Complete   *TurnMeta
	Session    *ChatSessionInfo
	Permission *PermissionRequest
	Terminal   *TerminalRequest

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

// Terminal op vocabulary (B1, gap G6): the five ACP terminal/* methods a
// TerminalRequest/TerminalResponse pair can broker. Mirrors the ACP method
// names (terminal/create → "create", etc.) without importing the ACP SDK
// into this package (same discipline as ContentBlock.Kind / MCPTransport's
// plain-string vocabularies).
const (
	TerminalOpCreate      = "create"
	TerminalOpOutput      = "output"
	TerminalOpWaitForExit = "wait_for_exit"
	TerminalOpKill        = "kill"
	TerminalOpRelease     = "release"
)

// TerminalRequest is a forwarded engine terminal/* request (B1, gap G6): the
// engine wants ctxloom to broker ONE terminal operation to the connected
// upstream editor — ctxloom implements no terminal of its own, ever. ID
// correlates the eventual TerminalResponse (unique within one chat, same
// discipline as PermissionRequest.ID). Op names which ACP terminal/* method
// this is (the TerminalOp* constants above). Params carries that method's
// ACP request body VERBATIM as JSON, WITH THE SESSION ID STRIPPED: the id in
// there is the CLIENT-role driver's own opaque session with the ENGINE,
// which the upstream editor does not share and must never see — the
// agent-role broker (internal/acpagent) substitutes ITS OWN editor-facing
// session id before relaying, exactly as permissionRequestWire substitutes
// sess.id for a forwarded permission request rather than carrying the
// engine's own id through. Params/Result ride as raw JSON (not five
// duplicated typed structs, one per op, in both this package and its proto
// mirror) — the same established pattern as PermissionRequest.ToolInput and
// ChatEvent.Raw: this hub layer relays bytes, it never needs to construct or
// validate the ACP-typed terminal shapes itself.
type TerminalRequest struct {
	ID     string
	Op     string
	Params json.RawMessage
}

// TerminalResponse resolves the TerminalRequest with the same ID: on
// success, Result carries the op's ACP response body verbatim as JSON
// (e.g. `{"terminalId":"..."}` for create, `{}` for kill/release); on
// failure (the editor declined, errored, or the brokering channel died
// before an answer arrived), Error carries a human-readable reason and
// Result is empty. Exactly one of Result/Error is meaningful — never both
// empty, which would be this codebase's signature silent-no-op bug wearing
// a new hat.
type TerminalResponse struct {
	ID     string
	Result json.RawMessage
	Error  string
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
	// initialize-time loadSession capability; internal/acp/session.go). It is
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
