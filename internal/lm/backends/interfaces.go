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
	SkillRegistry    = agent.SkillRegistry
	ContextProvider  = agent.ContextProvider
	MCPServer        = agent.MCPServer
	MCPManager       = agent.MCPManager
	SessionHistory   = agent.SessionHistory
	Session          = agent.Session
	SessionMeta      = agent.SessionMeta
	SessionEntry     = agent.SessionEntry
	PlanFile         = agent.PlanFile
	SetupRequest     = agent.SetupRequest
	ExecuteRequest   = agent.ExecuteRequest
	ExecuteResult    = agent.ExecuteResult
)

const (
	ModeInteractive = agent.ModeInteractive
	ModeOneshot     = agent.ModeOneshot

	EntryTypeUser       = agent.EntryTypeUser
	EntryTypeAssistant  = agent.EntryTypeAssistant
	EntryTypeToolUse    = agent.EntryTypeToolUse
	EntryTypeToolResult = agent.EntryTypeToolResult
	EntryTypeSystem     = agent.EntryTypeSystem
)
