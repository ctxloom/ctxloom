package agent

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// Backend is the LAUNCH facet of an agent — running the LLM and its session
// lifecycle. It is deliberately separate from the SettingsWriter (settings)
// facet so a consumer can depend on one without the other. Each agent
// (claude/antigravity) implements both facets; ltk implements/consumes only
// SettingsWriter.

// BackendConfig is the decoded, typed configuration for one labeled LLM entry.
// Each agent owns a concrete struct implementing it; shared code carries the
// interface and never type-switches on the backend. It is part of the
// engine-agnostic contract alongside Backend.
type BackendConfig interface {
	// BackendType returns the discriminator (claude-code / antigravity / codex)
	// naming the backend this config drives.
	BackendType() string
}

// ExecutionMode defines how the backend should execute.
type ExecutionMode int32

const (
	ModeInteractive ExecutionMode = 0 // Full interactive session
	ModeOneshot     ExecutionMode = 1 // Single prompt/response, exit after
)

// Fragment is one piece of context — a bundle fragment — with its metadata. It
// is the unit of context an agent injects into the model (via SetupRequest.
// Fragments); it has nothing to do with slash commands/skills, which travel
// separately as ManagedConfig.Skills ([]CommandExport).
type Fragment struct {
	Name         string
	Version      string
	Tags         []string
	Content      string
	Installation string // Setup/installation instructions for tooling
	IsDistilled  bool
	DistilledBy  string
}

// ModelInfo contains information about the model used for the response.
type ModelInfo struct {
	ModelName    string
	ModelVersion string
	Provider     string
}

// Backend is the core contract the runner (agent server) depends on: identify
// the agent, declare its modes, run the Setup→Execute→Cleanup lifecycle, and
// expose session history (read by the host for /clear recovery and compaction).
//
// It deliberately does NOT carry the hook/skill/context/MCP capability
// accessors: those are an agent's internal setup wiring, not something the runner
// calls, so forcing them onto every backend was a nil-returning contract nobody
// consumed.
type Backend interface {
	// Identity
	Name() string
	Version() string
	SupportedModes() []ExecutionMode

	// History exposes conversation history (transcripts) and /clear recovery.
	// The host reads it via the agent server and the compactor.
	History() SessionHistory

	// Execution lifecycle
	Setup(ctx context.Context, req *SetupRequest) error
	Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error)
	Cleanup(ctx context.Context) error
}

// ContextProvider manages getting context into the LLM's awareness.
// Implementation varies: CLI args, files, hooks, env vars, stdin, etc.
type ContextProvider interface {
	// Provide makes context available to the LLM.
	// The provider handles the transport mechanism internally.
	Provide(workDir string, fragments []*Fragment) error
	// Clear removes any provided context.
	Clear(workDir string) error
}

// SessionHistory provides access to the LLM's conversation history and tracks
// sessions for /clear recovery. Combines reading transcripts with tracking
// which sessions belong to which ctxloom run.
// Implementation varies by backend: JSONL files (Claude/Antigravity), etc.
type SessionHistory interface {
	// Reading sessions
	// GetCurrentSession returns the current/most recent session transcript.
	GetCurrentSession(workDir string) (*Session, error)
	// ListSessions returns available session metadata.
	ListSessions(workDir string) ([]SessionMeta, error)
	// GetSession returns a specific session by ID.
	GetSession(workDir string, sessionID string) (*Session, error)
	// GetSessionByPath returns a session by its transcript file path.
	GetSessionByPath(path string) (*Session, error)

	// Tracking for /clear recovery
	// TranscriptPathFromHook extracts or computes the transcript path from hook input.
	// Claude: computes path from sessionID + workDir
	// Antigravity/Codex: returns transcriptPath directly
	TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string

	// Note: "which session is previous" is resolved by ctxloom from its session
	// index (operations.ResolvePreviousSession), not by the agent — the index is
	// the authority for ordering, agent-of-origin, and cross-agent routing. The
	// agent only materializes a given session id (GetSession).
}

// Session represents a conversation session with normalized entries.
type Session struct {
	ID        string
	StartTime time.Time
	EndTime   time.Time
	Entries   []SessionEntry
}

// SessionMeta contains metadata about a session without full content.
type SessionMeta struct {
	ID         string
	StartTime  time.Time
	EndTime    time.Time
	EntryCount int
	// Path is the absolute path to the backend's raw transcript file, when
	// the backend stores one. Empty for backends without file-backed
	// transcripts. Lets callers scan the raw bytes (which include entries
	// the normalized parser drops, e.g. Claude Code `attachment` blocks)
	// without re-deriving the backend's private path convention.
	Path string
}

// PlanFile is one plan document from a session's ctxloom session directory
// (`~/.ctxloom/sessions/<harp>/<name>.plan.md`), served by the agent server so
// ctxloom can fold a session's plans into its distilled output (and carry them
// across a cross-agent handoff). A plain value DTO that crosses the wire.
type PlanFile struct {
	Name    string // descriptive base name (no .plan.md extension)
	Content string // the plan document's verbatim markdown
}

// SessionEntry represents a single turn in the conversation.
type SessionEntry struct {
	Timestamp  time.Time
	Type       SessionEntryType
	Content    string          // Text content for user/assistant messages
	ToolName   string          // For tool_use/tool_result entries
	ToolInput  json.RawMessage // For tool_use entries
	ToolOutput string          // For tool_result entries
	IsError    bool            // For tool_result entries
}

// SessionEntryType identifies the type of session entry.
type SessionEntryType string

const (
	EntryTypeUser      SessionEntryType = "user"
	EntryTypeAssistant SessionEntryType = "assistant"
	// EntryTypeThinking is the model's extended-thinking / reasoning prose. It is
	// distinct from assistant text so a frontend can style or toggle it separately
	// (the content is the reasoning, never tool fields). Only emitted when the
	// backend model has extended thinking enabled.
	EntryTypeThinking   SessionEntryType = "thinking"
	EntryTypeToolUse    SessionEntryType = "tool_use"
	EntryTypeToolResult SessionEntryType = "tool_result"
	EntryTypeSystem     SessionEntryType = "system"
)

// SetupRequest contains everything needed to prepare the backend before execution.
type SetupRequest struct {
	WorkDir   string
	Fragments []*Fragment // context fragments (bundle pieces) to inject
	Env       map[string]string
	Verbosity uint32
	// Managed is the host-assembled config/bundle setup payload. The host
	// resolves ctxloom config, profiles, and bundles and hands the result here
	// so the backend plugin never imports ctxloom config/bundles. Nil for
	// skip_setup/distill paths.
	Managed *ManagedConfig
}

// ManagedConfig is the host-assembled setup payload: ctxloom config, profile,
// and bundle state resolved host-side and handed to the backend's Setup so the
// plugin never imports ctxloom config/bundles. Hooks is the
// config+default-profile+bundle hook set WITHOUT context-injection; the agent
// appends its own context-injection hook from its plugin-side context hash. The
// command exports in Skills already have the target agent's enablement +
// metadata resolved host-side.
type ManagedConfig struct {
	Skills           []CommandExport           // per-target-agent skill (slash-command) exports
	Hooks            *wire.HooksConfig         // config + default-profile + bundle hooks (no context-injection)
	MCP              *wire.MCPConfig           // merged config + default-profile MCP servers
	BundleMCP        map[string]wire.MCPServer // MCP servers shipped by profile + builtin bundles (parallel to Hooks' bundle set)
	ManageStatusline bool                      // whether ctxloom manages the backend statusline
}

// ExecuteRequest contains the runtime parameters for execution.
type ExecuteRequest struct {
	Prompt *Fragment
	// WorkDir is the working directory the run executes in (the child engine's
	// cwd). It makes cwd a first-class Execute input, decoupled from Setup: the
	// runner applies it before Execute on EVERY path — including SkipSetup
	// oneshot/map/weave fan-out, where Setup (and its SetWorkDir) never runs — so
	// the passed workspace always reaches the child instead of defaulting to the
	// plugin's inherited ".". Empty means "unset" (BaseBackend.WorkDir → ".").
	WorkDir     string
	Mode        ExecutionMode
	Model       string
	Env         map[string]string
	Verbosity   uint32
	DryRun      bool
	Permissions PermissionMode
	Temperature float32
	SkipSetup   bool // Minimal mode - skip hooks/skills/context in backend

	// Stdin and Resize carry the frontend's terminal input into an interactive
	// run (over the bidi Run stream): Stdin is the keystroke byte stream, Resize
	// the terminal-size changes. Both nil for non-interactive/oneshot runs.
	Stdin  io.Reader
	Resize <-chan WindowSize
}

// ExecuteResult contains the outcome of execution.
type ExecuteResult struct {
	ExitCode  int32
	ModelInfo *ModelInfo
}
