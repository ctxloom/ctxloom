package agent

import (
	"fmt"
	"strings"
)

// This file defines the APPROACH ENUMS the surface-selection builder (cells.go)
// dispatches on, plus the shared, DATA-driven
// ApproachTable each backend declares its per-provider dispatch through. Every
// `.WithX()` on the builder takes a per-surface, caller-facing enum (ContextWrite
// / MCPWrite / SettingsWrite / CommandsWrite) whose race-capable values are
// UNSAFE-NAMED: choosing UnsafeFile is the caller's loud acknowledgment that
// ctxloom does not lock projects, so a live shared-cwd session may be using that
// exact well-known file. Each per-surface enum converts (via the unexported
// approach() method) to the single shared dispatch key, Approach, that
// SurfaceSet.SupportedApproaches / DefaultApproach / SurfaceFor key on —
// per-surface enum types make a cross-surface mismatch (an MCP approach on the
// context surface) a compile error, while the shared Approach type is what the
// per-provider dispatch tables actually key on.

// Approach is the delivery MECHANISM a caller selects for one surface — the
// engine-specific WAY that surface's bytes reach the model. It is the dispatch key
// SurfaceSet.SupportedApproaches / DefaultApproach / SurfaceFor / SharedRealization
// key on; callers never construct one directly — they name a per-surface enum
// value (ContextWrite / MCPWrite / SettingsWrite / CommandsWrite), which converts to
// this shared space via its approach() method.
type Approach int

const (
	// ApproachUnsafeFile writes the engine's native, well-known file the engine
	// reads directly (CLAUDE.md, AGENTS.md, .kiro/steering, .mcp.json,
	// settings.json / config.toml, command/skill dirs…). "unsafe" names itself
	// LOUDLY here because choosing it IS the race acknowledgment: a well-known
	// write into a SHARED live cwd cannot be locked against a concurrent session
	// using those exact files. Into an isolated (private) cell it is always safe —
	// isolation IS the conversion.
	ApproachUnsafeFile Approach = iota
	// ApproachSystemPrompt writes claude's framed context to an out-of-cwd scratch
	// file consumed via --append-system-prompt-file — semantically distinct from a
	// native file (the content enters the system prompt, not project memory).
	// claude context only.
	ApproachSystemPrompt
	// ApproachHook delivers context via a SessionStart inject-context hook reading
	// a regenerated cache file — COUPLED to the settings surface (the hook rides
	// it). claude/codex context only.
	ApproachHook
)

// String renders the approach for diagnostics and error text. The five
// per-surface enums below (ContextWrite, MCPWrite, SettingsWrite,
// CommandsWrite, SkillsWrite) used to each carry their own String() forwarding
// to this one via approach() — all five were test-only (error text that
// actually renders an approach goes through THIS method, via approach()), so
// they were deleted. Their approach() converters stay: they
// are the compile-time cross-surface guard SupportedApproaches/Build rely on.
// An UNDECLARED value renders as "unknown(<n>)", never as the zero value: the
// zero value here is the LEAST safe approach, so rendering it for an
// unrecognised one would make error text ("approach unsafe-file not supported")
// name a decided unsafe choice the caller never made. SurfaceKind.String in
// cells.go spells the same honest answer.
func (a Approach) String() string {
	switch a {
	case ApproachUnsafeFile:
		return "unsafe-file"
	case ApproachSystemPrompt:
		return "system-prompt"
	case ApproachHook:
		return "hook"
	default:
		return fmt.Sprintf("unknown(%d)", int(a))
	}
}

// ParseApproach is Approach.String's inverse, beside it for the same reason
// ParseSurfaceKind sits beside SurfaceKind.String: the names are now typed by
// users, so a rename has to move both halves at once.
//
// An unrecognised name is an ERROR, never a fallback. ApproachUnsafeFile is
// iota 0 AND is the least safe approach — the one whose own doc says choosing
// it IS the race acknowledgment — so a typo resolving to the zero value would
// silently elect a well-known shared-cwd write that nobody asked for.
func ParseApproach(s string) (Approach, error) {
	for _, a := range approachOrder {
		if a.String() == s {
			return a, nil
		}
	}
	return 0, fmt.Errorf("unknown approach %q (known: %s)", s, strings.Join(ApproachNames(), ", "))
}

// approachOrder is the enumeration, least-safe first, mirroring the const block
// above. It is the ONE list: ParseApproach, error text, --help and shell
// completion all read it rather than restating the names, so an approach added
// to the enum reaches every one of them without a second edit.
var approachOrder = []Approach{ApproachUnsafeFile, ApproachSystemPrompt, ApproachHook}

// ApproachNames lists every approach's label, in approachOrder.
func ApproachNames() []string {
	names := make([]string, 0, len(approachOrder))
	for _, a := range approachOrder {
		names = append(names, a.String())
	}
	return names
}

// ContextWrite names HOW the context surface is delivered. The caller ALWAYS
// states the approach explicitly (there is no silent default at the call site);
// the backend validates the choice against SupportedApproaches(SurfaceContext) at
// Build().
type ContextWrite int

const (
	// ContextWriteUnsafeFile writes the engine's native context file (CLAUDE.md /
	// AGENTS.md / .kiro/steering). agy/kiro's only approach; claude's and codex's
	// native-file option (codex has none — see codex's SupportedApproaches).
	ContextWriteUnsafeFile ContextWrite = iota
	// ContextWriteSystemPrompt writes claude's framed context to an out-of-cwd
	// scratch file passed via --append-system-prompt-file. claude only.
	ContextWriteSystemPrompt
	// ContextWriteHook delivers context via the SessionStart inject-context hook
	// carried in the settings surface. claude/codex only.
	ContextWriteHook
)

// approach converts the caller-facing enum to the shared dispatch key.
func (c ContextWrite) approach() Approach {
	switch c {
	case ContextWriteSystemPrompt:
		return ApproachSystemPrompt
	case ContextWriteHook:
		return ApproachHook
	default:
		return ApproachUnsafeFile
	}
}

// MCPWrite names HOW the MCP surface is delivered. Every backend's MCP surface
// (where present — codex folds it into settings) has exactly one approach today,
// so the enum exists for symmetry with ContextWrite and as an extension point.
type MCPWrite int

// MCPWriteUnsafeFile writes the engine's native MCP file (.mcp.json,
// mcp_config.json, .kiro/settings/mcp.json).
const MCPWriteUnsafeFile MCPWrite = iota

// approach converts the caller-facing enum to the shared dispatch key.
func (m MCPWrite) approach() Approach { return ApproachUnsafeFile }

// SettingsWrite names HOW the settings/hooks surface is delivered (engines that
// fold MCP or context into it carry the whole folded file along).
type SettingsWrite int

// SettingsWriteUnsafeFile writes the engine's native settings/hooks file
// (.claude/settings.json, codex config.toml, .agents/hooks.json, kiro agent JSON).
const SettingsWriteUnsafeFile SettingsWrite = iota

// approach converts the caller-facing enum to the shared dispatch key.
func (w SettingsWrite) approach() Approach { return ApproachUnsafeFile }

// CommandsWrite names HOW the commands surface is delivered. Every engine has
// exactly one approach (the native command/skill dir).
type CommandsWrite int

// CommandsWriteUnsafeFile writes the engine's native commands/skill dir
// (.claude/commands/, $CODEX_HOME/prompts, .agents/skills/, .kiro/skills/).
const CommandsWriteUnsafeFile CommandsWrite = iota

// approach converts the caller-facing enum to the shared dispatch key.
func (w CommandsWrite) approach() Approach { return ApproachUnsafeFile }

// SkillsWrite names HOW the Agent Skills surface (SKILL.md packages) is
// delivered. Like commands, every engine that supports it has exactly one
// approach today (the native skills dir) — no out-of-cwd flag exists for a
// skill package, so a SHARED-cwd delivery always falls back to the loud
// well-known write, same as commands.
type SkillsWrite int

// SkillsWriteUnsafeFile writes the engine's native skills dir (claude
// .claude/skills/<name>/, kiro .kiro/skills/<name>/, …).
const SkillsWriteUnsafeFile SkillsWrite = iota

// approach converts the caller-facing enum to the shared dispatch key.
func (w SkillsWrite) approach() Approach { return ApproachUnsafeFile }

// ApproachTable is a backend's DECLARED per-surface approach support — the data
// half of the per-provider dispatch. Each backend declares one as a literal in
// its own surfaces.go (the design's "backend advertises its support"): the
// per-kind slice lists the supported approaches with the FIRST entry as the
// backend's default (what WithEverything selects); a kind absent from the table
// is absent/folded for that backend (codex's MCP rides its config surface).
// The mechanical method bodies (SupportedApproaches / DefaultApproach /
// SurfaceFor) are shared here so only the TABLES differ per backend — never a
// re-implemented switch.
type ApproachTable map[SurfaceKind][]Approach

// Supported returns the declared approaches for kind (nil = kind absent/folded),
// implementing SurfaceSet.SupportedApproaches over the table.
func (t ApproachTable) Supported(kind SurfaceKind) []Approach { return t[kind] }

// Default returns the FIRST declared approach for kind — the backend's default —
// or false when kind is absent/folded, implementing SurfaceSet.DefaultApproach
// over the table.
func (t ApproachTable) Default(kind SurfaceKind) (Approach, bool) {
	if a := t[kind]; len(a) > 0 {
		return a[0], true
	}
	return 0, false
}

// TableDispatch is the embeddable carrier for the two PURELY mechanical halves
// of SurfaceSet's per-provider dispatch. Every backend's SupportedApproaches and
// DefaultApproach are the same one-liner over that backend's own ApproachTable,
// so writing the pair per backend gives ten bodies (five backends x two
// methods) with nothing but a table variable name distinguishing them — ten
// bodies free to drift from the contract cells.go states for them.
//
// A backend embeds it in its Surfaces value and sets Table to its own literal;
// the two interface methods then come for free, and only the TABLE is per
// backend — which is what ApproachTable's doc already claims. SurfaceFor stays
// per backend: claude's context arm is not purely kind-keyed, so it cannot be
// carried here.
//
// Behaviour is gated by TestApproachDispatch_* in internal/lm/backends, which
// holds every registered backend to the contract through the interface.
type TableDispatch struct{ Table ApproachTable }

// SupportedApproaches reports the backend's declared approaches for kind,
// implementing SurfaceSet.SupportedApproaches.
func (d TableDispatch) SupportedApproaches(kind SurfaceKind) []Approach {
	return d.Table.Supported(kind)
}

// DefaultApproach reports the backend's first-declared (default) approach for
// kind, implementing SurfaceSet.DefaultApproach.
func (d TableDispatch) DefaultApproach(kind SurfaceKind) (Approach, bool) {
	return d.Table.Default(kind)
}

// SurfaceFor resolves one (kind, a) against the table and the backend's
// kind→surface map, implementing the common skeleton of SurfaceSet.SurfaceFor:
// the approach is validated against the declared support, then the concrete
// surface is looked up. backend names the errors. A backend whose resolution is
// not purely kind-keyed (claude's multi-approach context) handles that arm itself
// before delegating here.
func (t ApproachTable) SurfaceFor(backend string, surfaces map[SurfaceKind]Delivery, kind SurfaceKind, a Approach) (Delivery, error) {
	if !containsApproach(t[kind], a) {
		return nil, fmt.Errorf("%s: no %s surface via %s", backend, kind, a)
	}
	d, ok := surfaces[kind]
	if !ok {
		return nil, fmt.Errorf("%s: no %s surface", backend, kind)
	}
	return d, nil
}
