package engine

import (
	"encoding/json"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// Session is the ENGINE-FACING projection of a resolved launch: what
// Instance is fed. It carries the identity, the label's configuration, the
// permission (already floored), the advised roots, the MCP endpoint and its
// bearer (the engine's MCP file names it), the prompt, the resume ref and
// the env additions. It carries NO package (already presentations), NO trust
// gate, NO isolation axes and NO coordinator credential: those were consumed
// by delivery or belong to the runner.
type Session struct {
	Identity   sessions.Identity
	Label      LabelConfig
	Mode       Mode
	Permission PermissionMode
	Roots      present.Paths // engine side of every root the launch advised
	WorkDir    string        // the cell's working directory
	Home       []HomeBinding // each home var the engine declares, resolved to its path under the session home
	MCP        sessions.Endpoint
	Prompt     string
	Resume     sessions.ResumeRef
	Env        map[string]string // engine PASSTHROUGH additions only; never ctxloom's own vars
}

// LabelConfig is one llm.configs label resolved: a configuration of the
// engine kind, not an engine identity. Several labels instantiate the same
// kind with different models, binaries or arguments.
type LabelConfig struct {
	Label  string
	Model  string
	Binary string
	Args   []string
}

// HomeBinding is one home var resolved: the engine sets Var to Path.
type HomeBinding struct{ Var, Path string }

// Items is the engine-facing projection of a composite package: the admitted
// items an engine's Exports decides over and Base.Delegate routes. The
// package layer produces it; engine does not import that layer.
type Items struct {
	Fragments []FragmentItem
	Commands  []CommandItem
	Skills    []SkillItem
	Hooks     []wire.Hook
	MCP       []wire.MCPServer
	Settings  bool // deny tools or statusline present
}

// FragmentItem is one fragment; a non-empty Premise makes it a PREFACE item
// (conditional), which Base.Delegate serves dynamically when the engine
// provides a dynamic approach.
type FragmentItem struct {
	Ref     string
	Name    string
	Body    []byte
	Premise string
}

// CommandItem is one command with this engine's opaque exports block.
type CommandItem struct {
	Ref     string
	Name    string
	Body    []byte
	Exports json.RawMessage // decoded against Definition.ExportSchema
}

// SkillItem is one skill package with this engine's opaque exports block.
type SkillItem struct {
	Ref     string
	Name    string
	Files   []SkillFile
	Exports json.RawMessage
}

// SkillFile is one file of a skill package.
type SkillFile struct {
	Path   string
	Digest string
	Size   int64
	Bytes  []byte
}

// Exports is what an engine says about a package: which commands become
// slash commands and how, which skills are enabled, how unified hook events
// route to native ones, and the native tool identifiers a deny list names.
type Exports struct {
	Commands  []CommandExport
	Skills    []SkillExport
	HookEvent map[string]string // unified event → native event; a unified event absent here is uncarried
	DenyTools []string
}

// CommandExport is one command in the engine's native slash-command shape.
type CommandExport struct {
	Name    string
	Body    []byte
	Enabled bool
	Meta    map[string]string
}

// SkillExport is one skill package as the engine enables it. Name and
// Description are the package's own frontmatter, carried so an engine's
// writer can refuse a package its rules reject before it emits.
type SkillExport struct {
	Name        string
	Description string
	Files       []SkillFile
	Enabled     bool
}
