package engine

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// DynamicApproach is the engine's provided way of consuming items served on
// the session's MCP endpoint: how the endpoint is named to the engine (the
// entry its MCP file carries). Optional on the Definition; Base.Delegate
// routes preface items to it when present.
type DynamicApproach interface {
	present.Approach
	Endpoint(ep sessions.Endpoint) wire.MCPServer
}

// The per-kind approach interfaces. Each embeds present.Approach (name and
// traits) and adds the typed Deliver for its kind, so the Definition's typed
// fields cannot receive another kind's approach. Deliver writes the kind's
// inputs under the advised start, at the root the plan selected, and
// reports where they landed on both sides; it is called ONLY by the static
// delivery adapter.

// ContextApproach delivers the engine's context surface.
type ContextApproach interface {
	present.Approach
	DeliverContext(start present.Start, root present.RootKind, in ContextInputs, fs afero.Fs) (present.Delivered, error)
}

// MCPApproach delivers the engine's MCP server config.
type MCPApproach interface {
	present.Approach
	DeliverMCP(start present.Start, root present.RootKind, in MCPInputs, fs afero.Fs) (present.Delivered, error)
}

// SettingsApproach delivers the engine's settings surface.
type SettingsApproach interface {
	present.Approach
	DeliverSettings(start present.Start, root present.RootKind, in SettingsInputs, fs afero.Fs) (present.Delivered, error)
}

// HooksApproach delivers the engine's hook registrations — a first-class
// surface even where the native form is a section of the settings file.
type HooksApproach interface {
	present.Approach
	DeliverHooks(start present.Start, root present.RootKind, in HooksInputs, fs afero.Fs) (present.Delivered, error)
}

// CommandsApproach delivers the engine's slash-command files.
type CommandsApproach interface {
	present.Approach
	DeliverCommands(start present.Start, root present.RootKind, in CommandsInputs, fs afero.Fs) (present.Delivered, error)
}

// SkillsApproach delivers the engine's Agent Skills packages.
type SkillsApproach interface {
	present.Approach
	DeliverSkills(start present.Start, root present.RootKind, in SkillsInputs, fs afero.Fs) (present.Delivered, error)
}

// Surfaces is the DERIVED approach table (Base.Surfaces()): per Kind, the
// one approach the engine declared. A Kind absent from the map is one the
// engine has no native form for: the planner REFUSES it (or records an
// accepted loss) — it is never a permitted no-op.
type Surfaces map[present.Kind]present.Approach

// The per-kind inputs, built by delivery from the decoded package.

// ContextInputs is the assembled context text and its content hash.
type ContextInputs struct {
	Text []byte
	Hash string
}

// MCPInputs is the MCP server set, the session's own endpoint included as a
// URL entry.
type MCPInputs struct{ Servers []wire.MCPServer }

// SettingsInputs is what the settings surface carries.
type SettingsInputs struct {
	DenyTools  []string
	Statusline bool
	Exports    Exports
}

// HooksInputs is the hook set and the unified→native event map.
type HooksInputs struct {
	Hooks     []wire.Hook
	HookEvent map[string]string
}

// CommandsInputs is the command export set.
type CommandsInputs struct{ Commands []CommandExport }

// SkillsInputs is the skill export set.
type SkillsInputs struct{ Skills []SkillExport }
