package gemini

import "github.com/ctxloom/ctxloom/internal/agent"

// Shims to the shared launch core (internal/agent) so the launch backend and
// capabilities read unchanged after the move from internal/lm/backends.
// Transitional — inline to agent.* later. The settings-writer shims (warn,
// computeHookHash, ...) live alongside the writer in gemini.go.
type (
	BaseBackend         = agent.BaseBackend
	BaseLifecycle       = agent.BaseLifecycle
	BaseMCPManager      = agent.BaseMCPManager
	BaseContextProvider = agent.BaseContextProvider

	LifecycleHandler = agent.LifecycleHandler
	SkillRegistry    = agent.SkillRegistry
	ContextProvider  = agent.ContextProvider
	MCPManager       = agent.MCPManager
	MCPServer        = agent.MCPServer
	SessionHistory   = agent.SessionHistory
	EventHandler     = agent.EventHandler
	ToolEvent        = agent.ToolEvent

	Skill            = agent.Skill
	Session          = agent.Session
	SessionMeta      = agent.SessionMeta
	SessionEntry     = agent.SessionEntry
	SessionEntryType = agent.SessionEntryType
	Fragment         = agent.Fragment

	SetupRequest   = agent.SetupRequest
	ExecuteRequest = agent.ExecuteRequest
	ExecuteResult  = agent.ExecuteResult
	ModelInfo      = agent.ModelInfo
	BackendConfig  = agent.BackendConfig

	CommandFileOption = agent.CommandFileOption
)

const (
	ModeInteractive = agent.ModeInteractive
	ModeOneshot     = agent.ModeOneshot

	EntryTypeUser      = agent.EntryTypeUser
	EntryTypeAssistant = agent.EntryTypeAssistant
	EntryTypeSystem    = agent.EntryTypeSystem

	BeforeToolUse = agent.BeforeToolUse
	AfterToolUse  = agent.AfterToolUse

	SCMContextFileEnv = agent.SCMContextFileEnv
)

var (
	NewBaseBackend         = agent.NewBaseBackend
	NewBaseLifecycle       = agent.NewBaseLifecycle
	NewBaseMCPManager      = agent.NewBaseMCPManager
	NewBaseContextProvider = agent.NewBaseContextProvider

	GetPromptContent = agent.GetPromptContent
	ResolveCommandFS = agent.ResolveCommandFS
)
