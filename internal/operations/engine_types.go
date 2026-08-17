package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file holds the frontend-neutral session-opener types: OpenEngineSession
// (engine_session.go) is their producer, and any ACP-shaped frontend (the ACP
// agent server package today; future non-ACP frontends like an in-process TUI
// or the VSCode extension) is a consumer. They live here — not in that
// frontend package — precisely so a frontend can depend on the opener without
// the opener depending back on any one frontend's wire protocol. The ACP
// agent server aliases these types (see its server.go, children.go, wire.go)
// so every existing reference through its own package keeps compiling
// unchanged.

// EngineChat is one live engine conversation backing a session: the
// assembled context to deliver on the first turn, the message-in/events-out
// channels of the engine's structured chat, and its teardown.
type EngineChat struct {
	// Context is ctxloom's assembled context for this session's cwd/profile,
	// prepended to the first prompt as a lead block ("" = none).
	Context string
	// In carries messages into the engine (user turns, permission answers,
	// turn cancels); the server never closes it except through Close.
	In chan<- agent.ChatMessage
	// Events streams the engine's normalized chat events; closed when the
	// conversation ends.
	Events <-chan agent.ChatEvent
	// Errs reports a conversation-fatal error (the pb chat error channel).
	Errs <-chan error
	// Close tears the engine conversation down (idempotent).
	Close func()
	// Harp is the ctxloom session name backing this conversation; when set it
	// becomes the ACP session id, which is what makes the session addressable
	// by session/load later. "" (accounting unavailable) falls back to a
	// connection-local generated id.
	Harp string
	// Modes surfaces ctxloom profile sets as ACP session modes (nil = none):
	// the composed defaults, each installed profile, and each agent's
	// composed profile set.
	Modes *SessionModes
	// AssembleMode re-assembles the lead context for a mode's profile set,
	// backing session/set_mode (nil = mode switching unsupported). A mode
	// switch changes the CONTEXT only — the engine is pinned at launch.
	AssembleMode func(ctx context.Context, mode SessionMode) (string, error)
	// Replay is the recorded history to replay to the client on session/load.
	Replay []agent.SessionEntry
	// LLMs advertises the ctxloom LLMs this session could run (nil = none). It
	// is ADVERTISEMENT ONLY — the engine's LLM is pinned at launch; a live
	// mid-session switch is not implemented (see modelState in acpagent's
	// wire.go).
	LLMs *SessionLLMs
	// WatchChildren subscribes this session to its delegated children's live
	// activity (D3, Tier A push): nil when no coordinator is
	// hosted (delegation degraded — the session behaves exactly as it did
	// pre-D3). The returned channel closes and cancel becomes a no-op once
	// ctx (the session's own lifetime) ends; the caller (acpagent's
	// pushChildUpdates) owns calling cancel exactly once.
	WatchChildren func(ctx context.Context) (<-chan ChildUpdate, func())
	// Commands surfaces ctxloom's OWN command system (B4, gap G5) as ACP's
	// available_commands_update — nil when the cwd has no commands
	// configured (the session advertises none). See SessionCommands's doc
	// for how this differs from IR3's engine-side passthrough.
	Commands *SessionCommands
	// InitSummary is ISO3/ISO4's SESSION INITIALIZATION SUMMARY text
	// (buildSessionInitSummary, engine_session.go) for this session — never
	// "" in production (OpenEngineSession computes it unconditionally for
	// every posture, including the fully unisolated common case). It
	// answers "what did ctxloom assemble on my behalf for this session?":
	// the resolved isolation posture (ISO3), and — widened in ISO4 — the
	// resolved engine/model, composed profiles, loaded fragments, available
	// commands/skills, and the MCP server set ctxloom configured, none of
	// which an editor or user can otherwise see (MCP status in particular
	// has NO other spec-legal home: it would otherwise ride `_meta`, which a
	// foreign client may ignore by contract). It crosses the
	// operations→acpagent boundary as plain data on this struct, the same
	// way Modes/LLMs/Commands already do, rather than riding the Events
	// channel: this is a fact about the SESSION, known before the engine is
	// even dialed, not a fact about any one TURN — and a frontend (acpagent)
	// that only ever drains Events from inside a turn (see server.go's
	// runTurn) would otherwise never see it until session/prompt ran once,
	// exactly the bug this field exists to fix. A frontend decides how (and
	// whether) to deliver it; acpagent emits it as a session/update
	// notification immediately after session/new|load, before replying —
	// see its emitSessionInitSummary (announce.go).
	InitSummary string
}

// SessionCommands surfaces ctxloom's OWN command system (bundle "commands" —
// internal/operations/commands.go's ListCommands/GetCommand, the same surface
// `ctxloom run --command <name>` and the MCP commands resource already
// expose) as ACP's available_commands_update (B4, gap G5): an editor driving
// `ctxloom acp` sees ctxloom's REAL commands in its command palette.
//
// This is deliberately separate from IR3's engine-side passthrough
// (ChatEvent.Raw forwarding a connected ENGINE's own available_commands_update
// verbatim — internal/acp/mapping.go's rawOnlyEvent / internal/acpagent/
// mapping.go's rawOnlyUpdates allowlist): that surfaces the underlying
// engine's commands (e.g. claude-code-acp's own slash commands, if it has
// any); THIS surfaces ctxloom's, in ctxloom's own agent role. A session can
// legitimately advertise both — they are not alternatives.
type SessionCommands struct {
	// Available lists ctxloom's own commands as of session open. nil/empty
	// means none configured.
	Available []CommandInfo
	// Resolve expands a recognized invocation into the text to actually send
	// the engine: name is matched EXACTLY against Available (never a prefix
	// or fuzzy match — an unmatched name is not this codebase's business to
	// guess at); rest is the free text the user typed after the command name
	// (ACP's AvailableCommandInput.Unstructured — "all text typed after the
	// command name"). ok is false when name does not match a known command —
	// the caller MUST leave the original prompt text untouched in that case:
	// most "/word ..." prompt text is just a user message that happens to
	// start with a slash (a file path, a fraction, casual punctuation), NOT a
	// command invocation, and misinterpreting it would silently corrupt the
	// user's own message — exactly the silent-no-op-adjacent failure this
	// codebase does not tolerate. A non-nil err means name DID match but
	// resolving its content failed (e.g. the bundle backing it disappeared
	// mid-session) — the caller surfaces that loudly rather than silently
	// sending the raw slash text through.
	Resolve func(ctx context.Context, name, rest string) (text string, ok bool, err error)
}

// CommandInfo is one of ctxloom's own commands, advertised to an ACP editor.
type CommandInfo struct {
	Name        string
	Description string
}

// SessionLLMs advertises the ctxloom LLM configs available to a session and
// which one launched. Informational: a client can display the engines and the
// current pick, but changing it mid-session is not supported (the engine is
// pinned at launch).
type SessionLLMs struct {
	Current   string
	Available []LLMInfo
}

// LLMInfo is one advertised LLM: its ctxloom config label (ID) and display name.
type LLMInfo struct {
	ID   string
	Name string
}

// SessionModes describes the profile-set-backed ACP session modes of a session.
type SessionModes struct {
	Current   string
	Available []SessionMode
}

// SessionMode is one selectable mode: a profile set to assemble — the composed
// default set, one ctxloom profile, or an agent's composed profiles.
type SessionMode struct {
	ID   string
	Name string
	// Profiles is the profile set this mode assembles; nil means the
	// configured defaults.
	Profiles []string
	// Engine is the mode's declared engine binding (agent modes; "" = none).
	// Informational at set_mode time: the session's engine is pinned at
	// launch, so a differing engine warns rather than switches.
	Engine string
}

// DefaultModeID is the synthetic mode representing the configured default
// profile set (which may compose several profiles, so no single profile name
// can stand for it).
const DefaultModeID = "default"

// OpenRequest describes the engine conversation a session needs.
type OpenRequest struct {
	// Cwd roots the session: ctxloom config is discovered from here.
	Cwd string
	// MCPServers are the client-supplied session/new mcpServers, passed
	// through to the engine conversation.
	MCPServers []agent.ChatMCPServer
	// Profile selects the ctxloom profile ("" = the configured defaults).
	Profile string
	// ResumeHarp names a recorded ctxloom session to resume (session/load):
	// the opener replays its history and primes the fresh engine with it.
	ResumeHarp string
	// FsUpstreamAddr, when non-empty, is the address of a local unix socket
	// the ACP AGENT role stood up so this session's engine conversation CAN
	// chain fs/read_text_file and fs/write_text_file upstream to the
	// connected editor, instead of local disk (B5, gap G14). "" means no
	// such upstream exists (the connected editor never declared the fs
	// capability, or this OpenRequest isn't coming from an ACP-hosted
	// session at all — e.g. `ctxloom acp run`/oneshot Execute never set
	// this field, and both keep reading local disk exactly as before this
	// field existed).
	//
	// OpenEngineSession forwards this into the engine's env (under
	// FsUpstreamEnvVar) ONLY when the RESOLVED axes are BOTH the fully
	// unisolated host case — see its doc comment for exactly where that gate
	// lives and why: a worktree- or container-bound session must never
	// chain (the editor's buffers describe a DIFFERENT tree in the worktree
	// case; container's same-path mount already makes local serving
	// correct) — see internal/acp/session.go's handleFsRead for the
	// consuming half of this same rule.
	FsUpstreamAddr string
	// ForwardTerminal asks the opened engine conversation to broker terminal/*
	// requests to the connected editor (B1, gap G6): the caller (acpagent.
	// Server) sets this from whatever THAT editor advertised at ITS OWN
	// initialize (clientCapabilities.terminal) — see agent.ChatRequest.
	// ForwardTerminal's doc comment for exactly what "true" honestly promises
	// and why this cannot default true. Rides straight through to the
	// engine's ChatRequest (OpenEngineSession).
	ForwardTerminal bool
}

// FsUpstreamEnvVar names the engine-env variable OpenEngineSession uses to
// forward OpenRequest.FsUpstreamAddr to the engine conversation (B5, gap
// G14). internal/acp/fsupstream.go mirrors this EXACT string literal rather
// than importing this package: internal/acp cannot import operations, because
// operations pulls in internal/lm/backends, which registers acp.NewACP() and
// so imports acp back. (agent.RuntimeContainerRootless/Rootful and their
// isolation counterparts are literal copies for the identical reason — see that const's
// doc in internal/shared/agent/chat.go.)
//
// The copies are NOT unbound. internal/acp/constants_binding_test.go
// — an external test package, which is outside that cycle and so may import
// both sides — asserts they are equal (TestFsUpstreamEnvVarMatchesOperations),
// so a rename on either side fails a test instead of silently serving fs/*
// from local disk.
const FsUpstreamEnvVar = "CTXLOOM_ACP_FS_UPSTREAM"

// ChildUpdateKind names what a ChildUpdate reports.
type ChildUpdateKind string

const (
	// ChildUpdateStarted marks a delegated child's run beginning.
	ChildUpdateStarted ChildUpdateKind = "started"
	// ChildUpdateMessage carries a chunk of the child's own output.
	ChildUpdateMessage ChildUpdateKind = "message"
	// ChildUpdateCompleted marks a delegated child's run ending.
	ChildUpdateCompleted ChildUpdateKind = "completed"
)

// ChildUpdate is one normalized, frontend-shaped notice about a delegated
// child's activity — cli's WatchChildren implementation (acpChildWatcher)
// translates coordinator AgentEvents into this shape; a frontend (acpagent's
// pushChildUpdates/childUpdateWire) only ever maps THIS onto its own wire,
// never the coordinator's own contract types.
type ChildUpdate struct {
	Harp string
	Kind ChildUpdateKind
	Text string
}
