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
// CollapsePlanIfUnenforced). The wire carries the String() form of a RESOLVED
// posture; PermissionNotRequested never crosses it.
type PermissionMode int

const (
	// PermissionNotRequested is the zero value: nobody asked for a posture.
	// It is not a mode an engine can run at — it exists so a caller that
	// leaves the field unset (launch.Source.Permission) is distinguishable
	// from one that asked for PermissionDefault by name. The resolution step
	// (agent.ResolveDefault) is the one place it becomes a posture.
	PermissionNotRequested PermissionMode = iota
	// PermissionDefault keeps the engine's normal in-tool approval prompting —
	// a human answers each request.
	PermissionDefault
	// PermissionAcceptEdits auto-accepts file edits but still prompts for the
	// rest (claude acceptEdits). Engines without a middle tier collapse it to
	// PermissionDefault.
	PermissionAcceptEdits
	// PermissionPlan is read-only / planning: the engine may inspect but not
	// mutate (claude's plan mode).
	PermissionPlan
	// PermissionBypass drops all in-engine prompting. The blast radius is
	// whatever contains the process — a real boundary, or nothing on the host.
	PermissionBypass
	// PermissionDontAsk never prompts: whatever the agent's rules do not
	// allow is denied (claude dontAsk).
	PermissionDontAsk
	// PermissionAuto hands each undecided call to the engine's own
	// classifier rather than a human (claude auto).
	PermissionAuto
)

// PermissionFloor is the posture every unhonourable declaration lands on: the
// most restrictive tier ctxloom can name. Read-only is the only answer to "the
// user asked for something we cannot honour" that cannot widen what they typed.
const PermissionFloor = PermissionPlan

// String renders the canonical wire/config spelling (claude-aligned).
func (m PermissionMode) String() string {
	switch m {
	case PermissionNotRequested:
		// Not a spelling ParsePermissionMode accepts: the zero must never
		// round-trip into a declaration.
		return "not requested"
	case PermissionDefault:
		return "default"
	case PermissionAcceptEdits:
		return "acceptEdits"
	case PermissionPlan:
		return "plan"
	case PermissionBypass:
		return "bypass"
	case PermissionDontAsk:
		return "dontAsk"
	case PermissionAuto:
		return "auto"
	default:
		// An out-of-range value must be visibly bad, not silently safe: a
		// corrupted wire value that rendered as "default" would be
		// indistinguishable from the real, intentional PermissionDefault.
		return "permissionMode(" + strconv.Itoa(int(m)) + ")"
	}
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

// ParsePermissionMode maps a config/CLI/wire string to a PermissionMode. It is
// lenient on case and accepts the common spellings. An empty or unrecognized
// value returns (PermissionNotRequested, false): the mode is never a posture
// when ok is false, so a caller that must tell "unset" from "misspelled" reads
// the string it passed (ResolveDefault does), not the returned mode.
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
	case "dontask", "dont-ask", "dont_ask":
		return PermissionDontAsk, true
	case "auto":
		return PermissionAuto, true
	default:
		return PermissionNotRequested, false
	}
}

// PermissionModeNames lists the accepted CLI/config values, for flag help and
// shell completion.
func PermissionModeNames() []string {
	return []string{"default", "acceptEdits", "plan", "bypass", "dontAsk", "auto"}
}

// WireMode parses a wire/config string, falling back to PermissionDefault for
// empty or unknown input — the fail-safe posture (nothing auto-happens; the
// engine prompts). It is not the most restrictive mode — plan permits less (no
// mutations at all) — but it is the safe default when intent is unknown. The
// sender put a resolved posture on the wire, so this is a fail-safe for a
// malformed frame, not a resolution step: it never returns
// PermissionNotRequested.
func WireMode(s string) PermissionMode {
	if m, ok := ParsePermissionMode(s); ok {
		return m
	}
	return PermissionDefault
}
