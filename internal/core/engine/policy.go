package engine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

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
	// ApproverReviewer hands what is left open to the engine's own reviewer
	// (a classifier), where the engine has one (PermissionModel.Reviewer).
	ApproverReviewer
)

// String renders the config spelling; an out-of-range value is visibly bad.
func (a Approver) String() string {
	switch a {
	case ApproverHuman:
		return "human"
	case ApproverNone:
		return "none"
	case ApproverReviewer:
		return "reviewer"
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
	case "reviewer":
		return ApproverReviewer, true
	default:
		return 0, false
	}
}

// ApproverNames lists the accepted spellings, for help and refusals.
func ApproverNames() []string {
	return []string{ApproverHuman.String(), ApproverNone.String(), ApproverReviewer.String()}
}

// DefaultApprovalTimeout is how long a request waits for the human when the
// agent declares no timeout; MaxApprovalTimeout is the longest an agent may
// declare. Expiry denies.
const (
	DefaultApprovalTimeout = 15 * time.Minute
	MaxApprovalTimeout     = 60 * time.Minute
)

// ErrApprovalTimeout refuses an approval_timeout spelling.
var ErrApprovalTimeout = errors.New("approval_timeout")

// ParseApprovalTimeout reads an approval_timeout spelling: a duration above
// zero and at most MaxApprovalTimeout.
func ParseApprovalTimeout(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%w %q is not a duration (e.g. 20m)", ErrApprovalTimeout, s)
	}
	if d <= 0 || d > MaxApprovalTimeout {
		return 0, fmt.Errorf("%w %q must be above 0 and at most %dm", ErrApprovalTimeout, s, int(MaxApprovalTimeout.Minutes()))
	}
	return d, nil
}

// PermissionPolicy is a session's resolved permission policy: the engine's
// posture (its own document, which only that engine reads), who answers
// what the posture leaves open, and the sandbox bounding the engine's own
// commands. It is resolved ONCE, at launch, from the human's config; the
// runner honours exactly what it is handed.
type PermissionPolicy struct {
	Posture  Posture
	Approver Approver
	// ApprovalTimeout is how long a request waits for the approver before
	// it is denied.
	ApprovalTimeout time.Duration
	Sandbox         Sandbox
	// Network lets the sandboxed commands reach the network.
	Network bool
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
	// Mode is the turn's starting posture, in the engine's own vocabulary
	// (PermissionModel.Postures); "" asks for none.
	Mode string
	// Grants are the session grants ctxloom holds for the run, as
	// engine-native rules.
	Grants []string
	// Trust is this turn's verdict on the repository.
	Trust WorkspaceTrust
}
