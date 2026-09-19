package engine

import (
	"strconv"
	"strings"
)

// PermissionMode is the launch-time permission posture ctxloom hands an
// engine. It mirrors claude's --permission-mode vocabulary so one vocabulary
// spans every engine; each engine maps it to its own mechanism, collapsing
// unsupported values to the nearest safe option. Not every engine implements
// every tier: an engine without a read-only tier collapses plan (see
// CollapsePlanIfUnenforced). The wire carries the String() form.
type PermissionMode int

const (
	// PermissionDefault keeps the engine's normal in-tool approval prompting —
	// a human answers each request. The zero value.
	PermissionDefault PermissionMode = iota
	// PermissionAcceptEdits auto-accepts file edits but still prompts for the
	// rest (claude acceptEdits). Engines without a middle tier collapse it to
	// PermissionDefault.
	PermissionAcceptEdits
	// PermissionPlan is read-only / planning: the engine may inspect but not
	// mutate (claude plan; codex --sandbox read-only).
	PermissionPlan
	// PermissionBypass drops all in-engine prompting. The blast radius is
	// whatever contains the process — a real boundary, or nothing on the host.
	PermissionBypass
)

// PermissionFloor is the posture every unhonourable declaration lands on: the
// most restrictive tier ctxloom can name. Read-only is the only answer to "the
// user asked for something we cannot honour" that cannot widen what they typed.
const PermissionFloor = PermissionPlan

// String renders the canonical wire/config spelling (claude-aligned).
func (m PermissionMode) String() string {
	switch m {
	case PermissionDefault:
		return "default"
	case PermissionAcceptEdits:
		return "acceptEdits"
	case PermissionPlan:
		return "plan"
	case PermissionBypass:
		return "bypass"
	default:
		// An out-of-range value must be visibly bad, not silently safe: a
		// corrupted wire value that rendered as "default" would be
		// indistinguishable from the real, intentional PermissionDefault.
		return "permissionMode(" + strconv.Itoa(int(m)) + ")"
	}
}

// AllowsWithoutPrompt reports whether the mode auto-allows a tool call with no
// human in the loop. Only PermissionBypass grants blanket allow; acceptEdits is
// edit-scoped and left to the engine's own flag, so it is not a blanket allow.
func (m PermissionMode) AllowsWithoutPrompt() bool {
	return m == PermissionBypass
}

// CollapsePlanIfUnenforced returns PermissionDefault in place of PermissionPlan
// when the engine cannot enforce plan as a genuine read-only mode, so plan
// never runs unrestrained on an engine without a read-only tier; any other
// mode is returned unchanged.
func (m PermissionMode) CollapsePlanIfUnenforced(engineEnforcesPlan bool) PermissionMode {
	if m == PermissionPlan && !engineEnforcesPlan {
		return PermissionDefault
	}
	return m
}

// SafeHeadless reports whether the mode can run with no human in the loop
// without hanging on an engine permission prompt: bypass (nothing prompts) and
// plan (read-only, nothing to approve). default and acceptEdits still block on
// non-edit tool calls, so a headless run must upgrade them to bypass.
func (m PermissionMode) SafeHeadless() bool {
	return m == PermissionBypass || m == PermissionPlan
}

// ParsePermissionMode maps a config/CLI/wire string to a PermissionMode. It is
// lenient on case and accepts the common spellings. An empty or unrecognized
// value returns (PermissionDefault, false) so callers can tell "unset" apart
// from an explicit "default" and apply their own fallback.
func ParsePermissionMode(s string) (PermissionMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "default":
		return PermissionDefault, true
	case "acceptedits", "accept-edits", "accept_edits":
		return PermissionAcceptEdits, true
	case "plan":
		return PermissionPlan, true
	case "bypass", "bypasspermissions", "dangerously-skip-permissions":
		return PermissionBypass, true
	default:
		return PermissionDefault, false
	}
}

// PermissionModeNames lists the accepted CLI/config values, for flag help and
// shell completion.
func PermissionModeNames() []string {
	return []string{"default", "acceptEdits", "plan", "bypass"}
}

// WireMode parses a wire/config string, falling back to PermissionDefault for
// empty or unknown input — the fail-safe posture (nothing auto-happens; the
// engine prompts). It is not the most restrictive mode — plan permits less (no
// mutations at all) — but it is the safe default when intent is unknown.
func WireMode(s string) PermissionMode {
	m, _ := ParsePermissionMode(s)
	return m
}
