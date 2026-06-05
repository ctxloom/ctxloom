package backends

import "github.com/ctxloom/ctxloom/internal/agent"

// The launch-facet (Backend) contract lives in internal/agent (the
// engine-agnostic core), alongside the settings-facet (SettingsWriter). These
// aliases keep existing backends/* and caller references working unchanged
// while the canonical definitions live in the core.
type (
	ExecutionMode    = agent.ExecutionMode
	Fragment         = agent.Fragment
	ModelInfo        = agent.ModelInfo
	Backend          = agent.Backend
	LifecycleHandler = agent.LifecycleHandler
	ToolEvent        = agent.ToolEvent
	EventHandler     = agent.EventHandler
	SkillRegistry    = agent.SkillRegistry
	Skill            = agent.Skill
	ContextProvider  = agent.ContextProvider
	MCPServer        = agent.MCPServer
	MCPManager       = agent.MCPManager
	SessionHistory   = agent.SessionHistory
	Session          = agent.Session
	SessionMeta      = agent.SessionMeta
	SessionEntry     = agent.SessionEntry
	SessionEntryType = agent.SessionEntryType
	SetupRequest     = agent.SetupRequest
	ExecuteRequest   = agent.ExecuteRequest
	ExecuteResult    = agent.ExecuteResult
)

const (
	ModeInteractive = agent.ModeInteractive
	ModeOneshot     = agent.ModeOneshot

	BeforeToolUse = agent.BeforeToolUse
	AfterToolUse  = agent.AfterToolUse

	EntryTypeUser       = agent.EntryTypeUser
	EntryTypeAssistant  = agent.EntryTypeAssistant
	EntryTypeToolUse    = agent.EntryTypeToolUse
	EntryTypeToolResult = agent.EntryTypeToolResult
	EntryTypeSystem     = agent.EntryTypeSystem
)
