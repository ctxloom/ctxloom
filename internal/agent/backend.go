package agent

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/ctxloom/shared/wire"
)

// Backend is the LAUNCH facet of an agent — running the LLM and its session
// lifecycle. It is deliberately separate from the SettingsWriter (settings)
// facet so a consumer can depend on one without the other. Each agent
// (claude/gemini) implements both facets; ltk implements/consumes only
// SettingsWriter.

// BackendConfig is the decoded, typed configuration for one labeled LLM entry.
// Each agent owns a concrete struct implementing it; shared code carries the
// interface and never type-switches on the backend. It is part of the
// engine-agnostic contract alongside Backend.
type BackendConfig interface {
	// BackendType returns the discriminator (claude-code / gemini / codex)
	// naming the backend this config drives.
	BackendType() string
}

// ExecutionMode defines how the backend should execute.
type ExecutionMode int32

const (
	ModeInteractive ExecutionMode = 0 // Full interactive session
	ModeOneshot     ExecutionMode = 1 // Single prompt/response, exit after
)

// Fragment represents a context fragment or prompt with metadata.
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

// Backend is the interface that all LLM backends must implement.
type Backend interface {
	// Identity
	Name() string
	Version() string
	SupportedModes() []ExecutionMode

	// Capability accessors - return nil if not supported.
	// These are CONCEPTS, not implementations. The returned objects
	// handle backend-specific details internally.
	Lifecycle() LifecycleHandler // Session events (start, end, tool use)
	Skills() SkillRegistry       // User-invokable actions
	Context() ContextProvider    // Getting context into the LLM
	MCP() MCPManager             // MCP server registration
	History() SessionHistory     // Conversation history and /clear recovery

	// Execution lifecycle
	Setup(ctx context.Context, req *SetupRequest) error
	Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error)
	Cleanup(ctx context.Context) error
}

// LifecycleHandler manages session lifecycle events.
// Implementation varies by backend: hooks (Claude/Gemini), callbacks, env vars, etc.
type LifecycleHandler interface {
	// OnSessionStart registers behavior for session start/resume/clear events.
	OnSessionStart(workDir string, handler EventHandler) error
	// OnSessionEnd registers behavior for session end events.
	OnSessionEnd(workDir string, handler EventHandler) error
	// OnToolUse registers behavior before/after tool invocations.
	OnToolUse(workDir string, event ToolEvent, handler EventHandler) error
	// Clear removes all ctxloom-managed lifecycle handlers.
	Clear(workDir string) error
}

// ToolEvent specifies when a tool-related event fires.
type ToolEvent int

const (
	BeforeToolUse ToolEvent = iota
	AfterToolUse
)

// EventHandler defines what happens when a lifecycle event fires.
type EventHandler struct {
	// Command to execute (if the backend supports command execution)
	Command string
	// Output function to call (for context injection)
	Output func() (string, error)
	// Timeout in seconds (0 means use default)
	Timeout int
}

// SkillRegistry manages user-invokable actions.
// Implementation varies: slash commands (Claude), MCP tools, keybindings, etc.
type SkillRegistry interface {
	// Register adds a skill that users can invoke.
	Register(workDir string, skill Skill) error
	// RegisterAll adds multiple skills at once.
	RegisterAll(workDir string, skills []Skill) error
	// Clear removes all ctxloom-managed skills.
	Clear(workDir string) error
	// List returns registered skill names.
	List(workDir string) ([]string, error)
}

// Skill represents a user-invokable action.
type Skill struct {
	Name        string   // Invocation name (e.g., "review", "commit")
	Description string   // What the skill does
	Content     string   // The prompt/action content
	Tags        []string // Categorization tags
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

// MCPServer represents an MCP server configuration to register.
type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// MCPManager manages MCP (Model Context Protocol) server registrations.
// Implementation varies by backend: settings.json (Claude/Gemini), config files, etc.
type MCPManager interface {
	// RegisterServer adds an MCP server to the backend configuration.
	RegisterServer(workDir string, server MCPServer) error
	// UnregisterServer removes an MCP server from the backend configuration.
	UnregisterServer(workDir string, name string) error
	// ListServers returns the names of registered MCP servers.
	ListServers(workDir string) ([]string, error)
	// GetServer returns the configuration for a specific MCP server.
	GetServer(workDir string, name string) (*MCPServer, error)
	// Clear removes all ctxloom-managed MCP servers.
	Clear(workDir string) error
	// Flush writes all pending MCP configuration changes.
	Flush(workDir string) error
}

// SessionHistory provides access to the LLM's conversation history and tracks
// sessions for /clear recovery. Combines reading transcripts with tracking
// which sessions belong to which ctxloom run.
// Implementation varies by backend: JSONL files (Claude), JSON files (Gemini), etc.
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
	// Gemini: returns transcriptPath directly
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
	EntryTypeUser       SessionEntryType = "user"
	EntryTypeAssistant  SessionEntryType = "assistant"
	EntryTypeToolUse    SessionEntryType = "tool_use"
	EntryTypeToolResult SessionEntryType = "tool_result"
	EntryTypeSystem     SessionEntryType = "system"
)

// SetupRequest contains everything needed to prepare the backend before execution.
type SetupRequest struct {
	WorkDir   string
	Fragments []*Fragment
	Prompts   []*Fragment // For slash commands/skills
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
// command exports in Prompts already have the target agent's enablement +
// metadata resolved host-side.
type ManagedConfig struct {
	Prompts          []CommandExport   // per-target-agent slash-command exports
	Hooks            *wire.HooksConfig // config + default-profile + bundle hooks (no context-injection)
	MCP              *wire.MCPConfig   // merged config + default-profile MCP servers
	ManageStatusline bool              // whether ctxloom manages the backend statusline
}

// ExecuteRequest contains the runtime parameters for execution.
type ExecuteRequest struct {
	Prompt      *Fragment
	Mode        ExecutionMode
	Model       string
	Env         map[string]string
	Verbosity   uint32
	DryRun      bool
	AutoApprove bool
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
