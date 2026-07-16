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
	// activity (D3, manly-grant (2), Tier A push): nil when no coordinator is
	// hosted (delegation degraded — the session behaves exactly as it did
	// pre-D3). The returned channel closes and cancel becomes a no-op once
	// ctx (the session's own lifetime) ends; the caller (acpagent's
	// pushChildUpdates) owns calling cancel exactly once.
	WatchChildren func(ctx context.Context) (<-chan ChildUpdate, func())
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
}

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
