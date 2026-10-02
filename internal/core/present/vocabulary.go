package present

import (
	"strings"

	"github.com/spf13/afero"
)

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
	// MCP is the engine's MCP server config (.mcp.json). An engine may fold MCP
	// into Settings.
	MCP
	// Settings is the engine's settings surface (.claude/settings.json).
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

// String renders the root kind as the label a binding selects it by.
func (r RootKind) String() string {
	switch r {
	case RootSessionHome:
		return "session-home"
	case RootProjectRoot:
		return "project-root"
	case RootWorkDir:
		return "work-dir"
	default:
		return "unknown"
	}
}

// ParseRootKind reads a root kind from its label; false for any other.
func ParseRootKind(label string) (RootKind, bool) {
	for _, r := range []RootKind{RootSessionHome, RootProjectRoot, RootWorkDir} {
		if r.String() == label {
			return r, true
		}
	}
	return 0, false
}

// ParseKind reads a surface kind from its label (Kind.String); false for
// any other.
func ParseKind(label string) (Kind, bool) {
	for _, k := range []Kind{Context, MCP, Settings, Hooks, Commands, Skills} {
		if k.String() == label {
			return k, true
		}
	}
	return 0, false
}

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

// Approach is the MARKER interface every per-kind approach embeds: the few
// shared parameters (a name, the declared Traits) and nothing else. The
// per-kind interfaces in engine (ContextApproach, MCPApproach, …) extend it
// with that kind's typed Deliver, so an approach for one kind cannot be
// assigned to another kind's field. The Traits the planner reads sit on the
// same value as the Deliver that honours them.
type Approach interface {
	Name() string
	Traits() Traits
}

// Delivered is what a typed Deliver reports: where the bytes landed on both
// sides, what was written, and how to undo it. A nil Undo means nothing was
// written.
//
// Claims are the values the approach puts into files it does not own whole,
// keyed by each file's host path. The approach writes nothing for them: the
// static writer stages them under its writer, so a file several writers put
// values into is written once, and each writer's values leave with it.
type Delivered struct {
	Presented Presentation
	Wrote     []string
	Undo      func(fs afero.Fs) error
	Claims    map[string][]Claim
}

// Claim is one value put at one place in a file. Pointer is an RFC 6901
// pointer into a file hew reads, Value anything hew encodes; a Pointer ending
// in "/-" claims an ELEMENT of that array, identified by its value. The empty
// Pointer claims the whole file and AppendedSection a section after the
// file's own text; Value is then the bytes. Via names what the claim came
// through — a companion — so a release can keep it.
type Claim struct {
	Pointer string
	Via     string
	Value   any
}

// AppendedSection is the Claim pointer for text appended after a file's own,
// separated from it by a blank line: a context file a user also writes.
const AppendedSection = "@section"

// A Claim pointer's segments are RFC 6901 keys, plus one form of hew's: a
// SELECTOR field=value names the first element of an array whose field holds
// value, an element without the field being selected by the empty value. A
// missing selected element is created holding the field. "~2" escapes a "="
// that is part of a key or value, as hew's own paths do.

// PointerKey is s as one key segment of a Claim pointer, "/" included.
func PointerKey(s string) string { return "/" + EscapeSegment(s) }

// PointerSelect is a selector segment of a Claim pointer, "/" included.
func PointerSelect(field, value string) string {
	return "/" + EscapeSegment(field) + "=" + EscapeSegment(value)
}

// EscapeSegment escapes s for one segment of a Claim pointer.
func EscapeSegment(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1", "=", "~2").Replace(s)
}

// UnescapeSegment reverses EscapeSegment.
func UnescapeSegment(s string) string {
	return strings.NewReplacer("~1", "/", "~2", "=", "~0", "~").Replace(s)
}
