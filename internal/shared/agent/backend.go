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
// (claude) implements both facets; ltk implements/consumes only
// SettingsWriter.

// BackendConfig is the decoded, typed configuration for one labeled LLM entry.
// Each agent owns a concrete struct implementing it; shared code carries the
// interface and never type-switches on the backend. It is part of the
// engine-agnostic contract alongside Backend.
type BackendConfig interface {
	// BackendType returns the discriminator (claude-code / codex)
	// naming the backend this config drives.
	BackendType() string
}

// VerbosityStep is what one repeated -v adds to the wire verbosity level that
// rides in RunOptions.Verbosity (llm.proto: 0=silent, 16=commands, 32=args,
// 48+=debug). Every launch path scales its -v COUNT by it, so no two paths can
// describe the same -vv to the engine differently.
const VerbosityStep = 16

// WireVerbosity converts a repeated-flag -v count into the wire verbosity level.
func WireVerbosity(count int) uint32 {
	if count <= 0 {
		return 0
	}
	return uint32(count * VerbosityStep)
}

// ExecutionMode defines how the backend should execute.
type ExecutionMode int32

const (
	ModeInteractive ExecutionMode = 0 // Full interactive session
	ModeOneshot     ExecutionMode = 1 // Single prompt/response, exit after
)

// Fragment is one piece of context — a bundle fragment — with its metadata. It
// is the unit of context an agent injects into the model (via SetupRequest.
// Fragments); it has nothing to do with slash commands, which travel
// separately as ManagedConfig.Commands ([]CommandExport).
//
// Only Content is ever read (base.go, contextfile.go, and codex/surfaces.go's
// WriteContextFile). Name/Version/Tags/IsDistilled/DistilledBy are write-only
// across the whole system and stay anyway: each is carried by llm.proto's
// Fragment message (fields 1,2,3,5,6), so removing one is a schema edit, not an
// implementer's call. The honest alternative is to add the consumer that
// justifies them (a per-fragment provenance header in the assembled context);
// that is a product decision, not a cleanup.
//
// A field with NO proto backing is a different case: it cannot have crossed the
// wire even in principle, so dropping it is a pure Go-side change.
type Fragment struct {
	Name        string
	Version     string
	Tags        []string
	Content     string
	IsDistilled bool
	DistilledBy string
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
// It deliberately does NOT carry the hook/command/context/MCP capability
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
// Implementation varies by backend: JSONL files (Claude), etc.
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
	// Codex: returns transcriptPath directly
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
	ID        string    `json:"id"`
	StartTime time.Time `json:"start_time"`
	// EndTime rides even when zero (a live/unfinished session): encoding/json's
	// omitempty never elides a zero-valued struct, so tagging it would be
	// misleading — a reader must check IsZero(), not field presence.
	EndTime    time.Time `json:"end_time"`
	EntryCount int       `json:"entry_count"`
	// Path is the absolute path to the backend's raw transcript file, when
	// the backend stores one. Empty for backends without file-backed
	// transcripts. Lets callers scan the raw bytes (which include entries
	// the normalized parser drops, e.g. Claude Code `attachment` blocks)
	// without re-deriving the backend's private path convention.
	Path string `json:"path,omitempty"`
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
	// Sidechain marks an entry that belongs to an engine's own in-harness
	// subagent (e.g. a Claude Code Task sidechain) rather than the session's
	// main thread. Backends set it when their transcript records the
	// distinction; false means main thread. Viewers use it to attribute
	// subagent-interior events; distillation and session-load replay keep to
	// the main thread via MainThreadEntries.
	Sidechain bool

	// --- IR2 (2026-07, ACP conformance audit gap G9): additive richness the
	// hub itself consumes or mediates. Every field below is OPTIONAL — a zero
	// value means "the producing backend didn't have one" — so no existing
	// producer or consumer needs to change to keep compiling or behaving
	// identically.

	// ToolCallID is the engine-NATIVE tool-call identifier (ACP
	// tool_call.toolCallId / tool_call_update.toolCallId), when the backend's
	// protocol exposes a stable one. Carried through the hub so a re-emission
	// can reuse the SAME id instead of inventing a fresh one keyed by tool
	// NAME — killing the same-name mispair risk a live drive confirmed
	// (internal/acpagent's FIFO push/pop-by-name assigns a fresh "call-N" per
	// tool name and pairs results in FIFO order; two concurrent calls to the
	// SAME tool can pop the wrong one's result). Empty means the backend
	// assigned none (or doesn't have the concept), and the hub falls back to
	// its own generated id exactly as before this field existed.
	ToolCallID string
	// ToolKind is the ACP tool-call category classification (execute | edit |
	// delete | move | read | search | fetch | think | switch_mode | other),
	// when the backend's protocol supplies one. Empty means unclassified.
	// Purely advisory (UI icon/treatment hints); mirrors
	// PermissionRequest.Kind's existing precedent.
	ToolKind string
	// ToolLocations are file locations (path + optional line) this tool call
	// touches — ACP's "follow-along" locations, for tool_use/tool_result
	// entries whose backend reports them. Nil when unreported.
	ToolLocations []ToolLocation
	// ToolContent carries a tool result's STRUCTURED content collection
	// (content blocks / diffs / terminal embeds) for tool_result entries,
	// alongside the flattened ToolOutput string (which stays populated
	// exactly as before — existing consumers that only want text are
	// unaffected). Nil when the backend reported only flattened output.
	ToolContent []ToolContentBlock
	// ContentBlocks carries this entry's content as a structured block list
	// (text/image/audio/resource), alongside the flattened Content string
	// (unchanged). Nil for entries whose backend only ever produced flat
	// text — every current producer. It exists so a richer producer (and
	// eventual multimodal intake, a later slice) has an additive IR
	// projection to write into instead of lossy-flattening at the mapping
	// boundary.
	ContentBlocks []ContentBlock
	// SystemKind discriminates what produced an EntryTypeSystem entry — there
	// are two producers today with no other way to tell them apart once the
	// IR has flattened Content to a string: an ACP `plan` update
	// (SystemKindPlan; Plan carries the structured entries, Content is a
	// rendered fallback for a consumer that only reads text) and the
	// delegated-turn-failure notice (internal/operations/delegate.go;
	// SystemKindNotice, the zero value, so every entry that predates this
	// field decodes as a notice exactly like before). A consumer that only
	// wants to know "is this content" (not which kind) can keep ignoring
	// SystemKind entirely and read Content, unchanged.
	SystemKind SessionSystemKind
	// Plan carries the ACP plan's structured entries when SystemKind ==
	// SystemKindPlan. Nil for every other system entry.
	Plan []PlanEntry
}

// SessionSystemKind discriminates the producer of an EntryTypeSystem entry.
// See SessionEntry.SystemKind.
type SessionSystemKind string

const (
	// SystemKindNotice is a freeform system notice with no structured payload
	// (e.g. internal/operations/delegate.go's delegated-turn-failure notice).
	// The zero value, so pre-existing entries (written before this field
	// existed) decode as notices, matching their actual prior behavior.
	SystemKindNotice SessionSystemKind = ""
	// SystemKindPlan marks a system entry produced from an ACP `plan` update;
	// Plan carries the structured entries.
	SystemKindPlan SessionSystemKind = "plan"
)

// PlanEntry is one task in an agent's execution plan — mirrors ACP's
// PlanEntry (content/priority/status) so the hub can carry it through
// structurally instead of flattening it to a checklist string at the mapping
// boundary (the IR2 fix for the conformance audit's headline finding: a
// prior revision only flattened a `plan` update into SessionEntry.Content,
// which had no field to carry entries back out, so a re-emission could never
// reconstruct a real ACP `plan` update — only make the flattened text
// visible as a message fallback).
type PlanEntry struct {
	Content  string // human-readable task description
	Priority string // ACP PlanEntryPriority: high | medium | low
	Status   string // ACP PlanEntryStatus: pending | in_progress | completed
}

// ToolLocation is one file location a tool call touches (ACP
// ToolCallLocation): Path is required, Line is 0 when unset (ACP's Line is
// itself optional and 1-based, so 0 is never a real line number).
type ToolLocation struct {
	Path string
	Line int
}

// ContentBlock is one structured ACP content block (text | image | audio |
// resource_link | resource). Kind/Text are a convenience projection for a
// consumer that only wants to know what it is or read its text; Raw is the
// block's own JSON verbatim so a consumer that understands the richer
// variants (image/audio bytes, resource contents — multimodal intake, a
// later slice) can decode it without this package needing to model every
// variant's fields today. Text is empty for a non-text kind.
type ContentBlock struct {
	Kind string
	Text string
	Raw  json.RawMessage
}

// ToolContentBlock is one element of a tool call's structured content
// collection (ACP ToolCallContent: content block | diff | terminal
// reference). Diff fields are decoded (not left in Raw) because ctxloom
// already flattens diffs for text display (internal/acp/mapping.go's
// toolContentText) and a diff-aware consumer wants the structured form
// without a second JSON round-trip; Raw carries the element verbatim
// regardless, for anything this type doesn't otherwise model.
// Canonical ToolContentBlock.Kind values.
//
// These are named BY PURPOSE, never after the vendor field that produced
// them, because the transcript policy layer (internal/transcript/policy)
// discriminates on them and a policy rule naming a vendor field is a defect:
// the same rule has to read correctly for claude, codex, kiro and whatever
// comes next.
//
// The vocabulary is deliberately NOT a taxonomy of every vendor shape. It is
// exactly the set of discriminators the policy needs, plus the generic
// KindContent for everything else — whose Raw still carries the vendor
// element verbatim, so nothing is lost by not having a name. Adding a kind
// per vendor shape would produce a catalogue that drifts the moment an engine
// ships a new tool.
const (
	// KindContent is the default: an element with no policy significance.
	// Its Raw holds the vendor element verbatim.
	KindContent  = "content"
	KindDiff     = "diff"
	KindTerminal = "terminal"

	// KindProcessOutput is a process's stdout/stderr/exit status.
	KindProcessOutput = "process_output"
	// KindFileSnapshot is a whole-file image, pre- or post-change.
	KindFileSnapshot = "file_snapshot"
	// KindToolCatalog is a listing of available tools.
	KindToolCatalog = "tool_catalog"
	// KindAgentResult is a delegated agent's outcome.
	KindAgentResult = "agent_result"
	// KindExcluded marks an element the policy withheld. It always replaces
	// the withheld element rather than removing it, so a reader can always
	// tell "policy withheld this" from "the tool returned nothing" — the
	// silent-omission shape this whole layer exists to stop.
	KindExcluded = "excluded"
)

type ToolContentBlock struct {
	// Kind is one of the Kind* constants above.
	Kind        string
	Text        string // flattened text, when Kind == KindContent
	DiffPath    string
	DiffOldText string // empty means "new file" (ACP's OldText is nil)
	DiffNewText string
	TerminalID  string
	Raw         json.RawMessage
}

// MainThreadEntries returns the entries that belong to the session's main
// thread, dropping subagent-interior (sidechain) ones. The single filter for
// consumers whose semantics are "the conversation the user had" — distillation
// and session-load replay — so they cannot drift from each other.
func MainThreadEntries(entries []SessionEntry) []SessionEntry {
	out := make([]SessionEntry, 0, len(entries))
	for _, e := range entries {
		if !e.Sidechain {
			out = append(out, e)
		}
	}
	return out
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
	// CellKind is the resolved isolation cell this run executes in, decided
	// host-side (isolation.Prepare) and carried over the wire. Setup does not
	// consume it yet (plan S4b); it is plumbed here so a later slice can switch
	// delivery on the cell instead of inferring it from WorkDir.
	CellKind CellKind
}

// ManagedConfig is the host-assembled setup payload: ctxloom config, profile,
// and bundle state resolved host-side and handed to the backend's Setup so the
// plugin never imports ctxloom config/bundles. Hooks is the
// config+default-profile+bundle hook set WITHOUT context-injection; the agent
// appends its own context-injection hook from its plugin-side context hash. The
// command exports in Commands already have the target agent's enablement +
// metadata resolved host-side.
type ManagedConfig struct {
	// Surfaces is the AGENT BINDING's delivery preference, one approach per
	// surface kind, already validated host-side against this engine's
	// SupportedApproaches. Empty takes the engine's default, which is every
	// agent that has not asked for anything.
	//
	// It lives on the binding rather than in the engine's approach table
	// because preference is a property of the CALLER, not the engine: measured
	// 2026-08-08, no single table order serves launch, materialize and at-rest
	// delivery at once. An agent is always launched, so it is the one caller
	// that can safely prefer system-prompt — the approach with no argv sink at
	// rest.
	Surfaces map[SurfaceKind]Approach

	Commands         []CommandExport           // per-target-agent command (slash-command) exports
	Skills           []SkillExport             // per-target-agent Agent Skill package exports (SurfaceSkills)
	Hooks            *wire.HooksConfig         // config + default-profile + bundle hooks (no context-injection)
	MCP              *wire.MCPConfig           // merged config + default-profile MCP servers
	BundleMCP        map[string]wire.MCPServer // MCP servers shipped by profile + builtin bundles (parallel to Hooks' bundle set)
	ManageStatusline bool                      // whether ctxloom manages the backend statusline
	// DenyTools is the config+default-profile deny_tools union (deny-tools.md
	// root-cause fix): per-engine tool identifiers the resolved profile set
	// denies at launch (e.g. "Task" — Claude Code's built-in sub-agent tool,
	// which ctxloom cannot mediate). Reaches only backends whose settings
	// surface has a native per-tool deny list (claude-code today); others
	// ignore it. Never trust-gated: a deny entry only narrows a launch, it
	// never executes anything.
	DenyTools []string
}

// ExecuteRequest contains the runtime parameters for execution.
type ExecuteRequest struct {
	Prompt *Fragment
	// WorkDir is the working directory the run executes in (the child engine's
	// cwd). It makes cwd a first-class Execute input, decoupled from Setup: the
	// runner applies it before Execute on EVERY path — including the SkipSetup
	// oneshot path, where Setup (and its SetWorkDir) never runs — so
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
	SkipSetup   bool // Minimal mode - skip hooks/commands/context in backend
	// CellKind is the resolved isolation cell this run executes in, decided
	// host-side (isolation.Prepare) and carried over the wire alongside SetupRequest.
	// A backend's buildArgs switches on it directly rather than inferring the
	// cell from WorkDir: claude gates its out-of-cwd launch flags
	// (--append-system-prompt-file / --mcp-config / --settings) on
	// CellKindShared, since an isolated cell reads the engine's well-known
	// files in its private cwd instead.
	CellKind CellKind

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
