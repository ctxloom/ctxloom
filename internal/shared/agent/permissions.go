package agent

import "strings"

// PermissionMode is the generalized launch-time permission posture ctxloom hands
// a backend. It mirrors claude's --permission-mode vocabulary (and ACP
// SessionModes) so one vocabulary spans every client; each backend maps it to
// its own mechanism, collapsing unsupported values to the nearest safe option.
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
	// mutate (claude plan; codex --sandbox read-only --ask-for-approval never).
	PermissionPlan
	// PermissionBypass drops all in-engine prompting (claude
	// --dangerously-skip-permissions; codex --full-auto). The blast radius is
	// whatever contains the process — a real boundary, or nothing on the host.
	PermissionBypass
)

// String renders the canonical wire/config spelling (claude-aligned).
func (m PermissionMode) String() string {
	switch m {
	case PermissionAcceptEdits:
		return "acceptEdits"
	case PermissionPlan:
		return "plan"
	case PermissionBypass:
		return "bypass"
	default:
		return "default"
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
// empty or unknown input (the fail-safe posture — the most restrictive mode).
func WireMode(s string) PermissionMode {
	m, _ := ParsePermissionMode(s)
	return m
}

// SafeHeadless reports whether the mode can run with no human in the loop
// without hanging on an engine permission prompt: bypass (nothing prompts) and
// plan (read-only, nothing to approve). default and acceptEdits still block on
// non-edit tool calls, so a headless ONESHOT must upgrade them to bypass.
func (m PermissionMode) SafeHeadless() bool {
	return m == PermissionBypass || m == PermissionPlan
}
