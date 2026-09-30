package engine

import (
	"strconv"
	"strings"
	"time"
)

// reach ranks the postures by how much an engine may do without a human
// saying yes: plan (read-only) < dontAsk (only what the rules allow) <
// default (the rules, plus whatever the approver allows) < acceptEdits
// (edits unasked) < auto (the engine's classifier decides) < bypass
// (nothing asked). It is what a ceiling is measured on; 0 is "not a
// posture" and is within nothing.
func (m PermissionMode) reach() int {
	switch m {
	case PermissionPlan:
		return 1
	case PermissionDontAsk:
		return 2
	case PermissionDefault:
		return 3
	case PermissionAcceptEdits:
		return 4
	case PermissionAuto:
		return 5
	case PermissionBypass:
		return 6
	default:
		return 0
	}
}

// Within reports whether m reaches no further than ceiling. Neither side
// may be the zero value or out of range: a posture nobody resolved is
// within nothing, and nothing is within it.
func (m PermissionMode) Within(ceiling PermissionMode) bool {
	r, c := m.reach(), ceiling.reach()
	return r != 0 && c != 0 && r <= c
}

// AfterPlanNames lists the postures an approved plan may continue at: a
// plan-first session's after_plan, and the modes an answer may switch to.
func AfterPlanNames() []string {
	return []string{PermissionDefault.String(), PermissionAcceptEdits.String()}
}

// Approver is who answers a request the agent's rules and posture leave
// open.
type Approver int

const (
	// ApproverHuman is the zero value, and the RULED default: an uncovered
	// request escalates to the human at the root session, and waits for
	// them until the approval timeout denies it.
	ApproverHuman Approver = iota
	// ApproverNone denies whatever the rules and posture leave open; nobody
	// is asked.
	ApproverNone
)

// String renders the config spelling; an out-of-range value is visibly bad.
func (a Approver) String() string {
	switch a {
	case ApproverHuman:
		return "human"
	case ApproverNone:
		return "none"
	default:
		return "approver(" + strconv.Itoa(int(a)) + ")"
	}
}

// ParseApprover maps a config spelling to an Approver, ignoring case and
// surrounding space. Empty is not a spelling: the caller decides what an
// undeclared approver means.
func ParseApprover(s string) (Approver, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "human":
		return ApproverHuman, true
	case "none":
		return ApproverNone, true
	default:
		return 0, false
	}
}

// ApproverNames lists the accepted spellings, for help and refusals.
func ApproverNames() []string { return []string{ApproverHuman.String(), ApproverNone.String()} }

// DefaultApprovalTimeout is how long a request waits for the human when the
// agent declares no timeout; MaxApprovalTimeout is the longest an agent may
// declare. Expiry denies.
const (
	DefaultApprovalTimeout = 15 * time.Minute
	MaxApprovalTimeout     = 60 * time.Minute
)

// PermissionPolicy is a session's resolved permission posture: where it
// starts, how far a plan approval or a mode change may take it, the
// engine-native rules it declares, and who answers what they leave open.
// It is resolved ONCE, at launch; the runner honours exactly what it is
// handed.
type PermissionPolicy struct {
	// Mode is the starting posture.
	Mode PermissionMode
	// AfterPlan, when provided, makes the session plan-first: Mode is
	// PermissionPlan, and an approved plan continues at this posture. It is
	// within Ceiling and never PermissionBypass.
	AfterPlan Declared[PermissionMode]
	// Ceiling is the furthest a plan approval or a mode change may take the
	// session — and the furthest a child it launches may start — capped at
	// the launching session's own ceiling.
	Ceiling PermissionMode
	// Allow, Deny and Ask are engine-native rules, validated by the
	// engine's approval codec.
	Allow, Deny, Ask []string
	Approver         Approver
	// ApprovalTimeout is how long a request waits for the approver before
	// it is denied.
	ApprovalTimeout time.Duration
}

// WorkspaceTrust is one turn's verdict on the repository the session works
// in: whether the repository's own executable surfaces may load.
type WorkspaceTrust int

const (
	// TrustUntrusted is the zero value, and the default: the repository's
	// own settings, hooks and MCP servers do not load.
	TrustUntrusted WorkspaceTrust = iota
	// TrustTrusted lets the repository's surfaces load.
	TrustTrusted
)

// TurnPosture is the permission posture one structured turn runs at: it
// rides Turn, so a posture that changes between turns (a plan approval, a
// session grant, a trust verdict) reaches the next turn's process.
type TurnPosture struct {
	// Mode is the turn's starting mode. Never PermissionBypass: bypass does
	// not change per turn and stays on the launch argv. The zero value asks
	// for no mode.
	Mode PermissionMode
	// Grants are the session grants ctxloom holds for the run, as
	// engine-native rules.
	Grants []string
	// Trust is this turn's verdict on the repository.
	Trust WorkspaceTrust
}
