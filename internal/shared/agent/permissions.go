package agent

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// PermissionMode is the generalized launch-time permission posture ctxloom hands
// a backend. It mirrors claude's --permission-mode vocabulary so one vocabulary
// spans every client; each backend maps it to its own mechanism, collapsing
// unsupported values to the nearest safe option. Not every backend implements
// every tier: the ACP chat driver distinguishes only bypass (allow-without-
// prompt), so plan/acceptEdits collapse to the reject decision there, and
// backends without a read-only tier collapse plan (see CollapsePlanIfUnenforced).
//
// It replaces the former AutoApprove bool: the old true maps to PermissionBypass,
// the old false to PermissionDefault. The wire carries the String() form.
type PermissionMode int

const (
	// PermissionDefault keeps the engine's normal in-tool approval prompting —
	// a human (or the ACP client) answers each request. The zero value.
	PermissionDefault PermissionMode = iota
	// PermissionAcceptEdits auto-accepts file edits but still prompts for the
	// rest (claude acceptEdits). Backends without a middle tier collapse it to
	// PermissionDefault.
	PermissionAcceptEdits
	// PermissionPlan is read-only / planning: the engine may inspect but not
	// mutate (claude plan; codex --sandbox read-only).
	PermissionPlan
	// PermissionBypass drops all in-engine prompting (claude
	// --dangerously-skip-permissions; codex
	// --dangerously-bypass-approvals-and-sandbox). The blast radius is
	// whatever contains the process — a real boundary, or nothing on the host.
	PermissionBypass
)

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
		// An out-of-range value used to fall through to "default" —
		// indistinguishable from the real, intentional PermissionDefault. A
		// corrupted wire value must be visibly bad, not silently safe.
		return fmt.Sprintf("permissionMode(%d)", int(m))
	}
}

// AllowsWithoutPrompt reports whether the mode auto-allows a tool call with no
// human in the loop — the chat/oneshot decision when permissions are not
// forwarded. Only PermissionBypass grants blanket allow; acceptEdits is
// edit-scoped and left to the engine's own flag, so it is not a blanket allow.
func (m PermissionMode) AllowsWithoutPrompt() bool {
	return m == PermissionBypass
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

// PermissionFloor is the posture every unhonourable declaration lands on: the
// most restrictive tier ctxloom can name. Read-only is the only answer to "the
// user asked for something we cannot honour" that cannot widen what they typed
// — the same floor the delegation path applies when a child's declared posture
// is not headless-safe.
const PermissionFloor = PermissionPlan

// ResolveDefault picks the posture from layered source spellings — the first
// DECLARED of the ordered sources wins (e.g. flag > agent > label) — falling
// back to bypass when nothing is declared and the backend is the claude-code
// host stopgap, else default. It is the run-context-independent base shared by
// the run resolver and `agent show`; callers layer CollapsePlanIfUnenforced and
// the headless floor on top. claudeCodeDefault is backendType == the claude-code
// backend type (the caller owns that comparison so this stays config-free).
//
// UNSET AND UNPARSEABLE ARE DIFFERENT INPUTS, and conflating them was a silent
// privilege escalation. An empty source declares nothing and is skipped, so a
// lower source (and ultimately the built-in default) answers for it. A NON-EMPTY
// source that does not parse is a declaration that MISSED: the user asked for
// something, so continuing down the chain hands them a posture nobody chose —
// on claude-code, the bottom of that chain is bypass, so `permissions: plann`
// (an obvious `plan`) resolved to full --dangerously-skip-permissions, and the
// escalation ladder derived from it flipped from auto-decline to auto-accept.
// A missed declaration therefore STOPS the chain, reports a fatal ClassConfig
// finding, and floors to PermissionFloor. honoured is false in exactly that
// case, so a caller must not apply any widening step (the ONESHOT floor, a
// backend collapse) to the returned mode: nothing may lift a posture nobody
// successfully declared. Degraded mode narrows here too — it never widens.
func ResolveDefault(sources []string, claudeCodeDefault bool) (mode PermissionMode, honoured bool) {
	for _, s := range sources {
		if strings.TrimSpace(s) == "" {
			continue
		}
		m, ok := ParsePermissionMode(s)
		if !ok {
			fallback := PermissionDefault
			if claudeCodeDefault {
				fallback = PermissionBypass
			}
			strictness.FailOnce(strictness.ClassConfig,
				fmt.Sprintf("set permissions: to one of %s (fix the typo in .ctxloom/config.yaml, the --permissions flag, or the --config-set override)", strings.Join(PermissionModeNames(), "|")),
				"unknown permissions value %q (known: %s); an unrecognised posture is NOT treated as unset — it would otherwise resolve to %q, so this run is floored to %q (read-only)",
				s, strings.Join(PermissionModeNames(), "|"), fallback, PermissionFloor)
			return PermissionFloor, false
		}
		return m, true
	}
	if claudeCodeDefault {
		return PermissionBypass, true
	}
	return PermissionDefault, true
}

// CollapsePlanIfUnenforced returns PermissionDefault in place of PermissionPlan
// when the backend cannot enforce plan as a genuine read-only mode, so plan
// never runs unrestrained on a backend without a read-only tier; any other mode
// is returned unchanged. Both the interactive run resolver and the headless
// fan-out apply it before deciding the final posture. backendEnforcesPlan comes
// from backends.EnforcesReadOnlyPlan.
func (m PermissionMode) CollapsePlanIfUnenforced(backendEnforcesPlan bool) PermissionMode {
	if m == PermissionPlan && !backendEnforcesPlan {
		return PermissionDefault
	}
	return m
}

// SafeHeadless reports whether the mode can run with no human in the loop
// without hanging on an engine permission prompt: bypass (nothing prompts) and
// plan (read-only, nothing to approve). default and acceptEdits still block on
// non-edit tool calls, so a headless ONESHOT must upgrade them to bypass.
func (m PermissionMode) SafeHeadless() bool {
	return m == PermissionBypass || m == PermissionPlan
}
