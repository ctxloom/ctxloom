package present

// Kind is a surface category: the CROSS-ENGINE union of every engine's
// surfaces. The set is closed. It is deliberately NOT a dispatch key — no
// code branches on a surface's kind to decide HOW to write it (that stays
// each approach's own Deliver). An engine may fold one kind's bytes into
// another kind's file (MCP into settings; hooks into settings on every
// shipped engine), and that is the engine's business: the kind is still its
// own kind, and a caller selecting the folded kind is a permitted no-op on
// that engine, never an error. Dynamic-only kinds (premise catalog, link
// mates, findings, resources) are delivery's vocabulary, not a Kind: they
// have no native file.
type Kind int

const (
	// Context is the engine's context surface (CLAUDE.md, .agents/AGENTS.md,
	// steering, or a context cache file).
	Context Kind = iota
	// MCP is the engine's MCP server config (.mcp.json, mcp_config.json,
	// .kiro/settings/mcp.json). An engine may fold MCP into Settings.
	MCP
	// Settings is the engine's settings surface (.claude/settings.json,
	// codex config.toml, kiro agent JSON).
	Settings
	// Hooks is the engine's hook registrations — the surface ltk and the
	// context-injection hook are delivered through. It is a Kind of its own
	// because it is a first-class delivered surface, even where the engine's
	// native form is a section of its settings file.
	Hooks
	// Commands is the engine's slash-command files; the export a caller hands
	// this surface is always a command, never a skill.
	Commands
	// Skills is the engine's Agent Skills surface — SKILL.md package
	// directories, loaded by the engine via progressive disclosure.
	Skills
)

// String renders the kind as the stable lowercase label used in delivery
// reports and typed by humans on the CLI.
func (k Kind) String() string {
	switch k {
	case Context:
		return "context"
	case MCP:
		return "mcp"
	case Settings:
		return "settings"
	case Hooks:
		return "hooks"
	case Commands:
		return "commands"
	case Skills:
		return "skills"
	default:
		return "unknown"
	}
}

// RootKind names a root an approach can write under. RootProjectRoot is the
// SHARED root: an approach offers it in its Traits and the binding SELECTS
// it per kind. When the binding selects it, the project root IS the target —
// a selected root, never a fallback the planner reaches for. The zero value
// names no root.
type RootKind int

const (
	RootSessionHome RootKind = iota + 1
	RootProjectRoot
	RootWorkDir
)

// Channel is HOW the engine is told about a delivered surface: a file it
// opens, an argv flag, or an environment variable.
type Channel int

const (
	ChannelFile Channel = iota + 1
	ChannelArgv
	ChannelEnv
)

// Traits are the declared facts about ONE approach: the roots it can write
// under (the first is its default; the binding may select another), which
// channel tells the engine, whether it is argv-only (no at-rest form) and
// whether it persists after exit. The planner reads these instead of probing
// a built approach.
type Traits struct {
	Roots      []RootKind
	Channel    Channel
	LaunchOnly bool
	Persists   bool
}

// Offers reports whether the traits list the root.
func (t Traits) Offers(r RootKind) bool {
	for _, x := range t.Roots {
		if x == r {
			return true
		}
	}
	return false
}
