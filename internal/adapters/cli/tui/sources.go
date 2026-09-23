package tui

import (
	"context"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// Sources are the overlay's data seams. The CLI wires them to the operations
// feed resolver, the session index, and the coordinator's roster; tests
// inject fakes.
type Sources struct {
	// Roster lists the observable sessions (index sessions merged with
	// coordinator-held children — see BuildRoster).
	Roster func(ctx context.Context) ([]RosterRow, error)
	// Watch opens a harp's observation feed (operations.WatchSessionFeed
	// behind a cancel).
	Watch func(ctx context.Context, harp string) (*Feed, error)
	// Control runs one control verb (steer, question, summarize, pause,
	// resume) against a harp as the HUMAN initiator, through the serving
	// coordinator (coord.Coordinator.Control). Nil when no coordinator is
	// hosted; every control key then says so rather than opening.
	Control func(ctx context.Context, req coord.ControlRequest) (coord.ControlResult, error)
	// Now is the export-filename clock; nil means time.Now.
	Now func() time.Time
	// PendingApprovals lists approvals parked for this human's decision
	// (coord.Coordinator.PendingApprovals). Nil disables the approvals
	// surface entirely (the "a" key hints unavailable rather than opening).
	PendingApprovals func() []coord.PendingApproval
	// AnswerApproval resolves one parked approval. decision is one of
	// DECISION_ACCEPT, DECISION_ACCEPT_FOR_SESSION, DECISION_DECLINE. note
	// travels to the child (may be empty). messageID+childHarp are latched
	// at the keypress that triggers the answer, exactly like Inject latches
	// its target harp.
	AnswerApproval func(messageID, childHarp string,
		decision agentcoordpb.ApprovalDecision_Decision, note string) error
}

func (s Sources) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// RosterRow is one line of the agents pane.
type RosterRow struct {
	Harp   string
	Agent  string
	Engine string
	// State is the roster vocabulary: the coordinator's coord.State* values
	// for the children it holds a state for; otherwise the session lock's
	// verdict as StateLive | StateEnded | StateUnknown.
	State  string
	Parent string
	Depth  int // lineage indent
}

// Feed is one open observation feed. Cancel releases the watch (switching
// harps, closing the overlay).
type Feed struct {
	// Source is what actually fed the stream: "live" or "store".
	Source string
	Events <-chan operations.SessionFeedEvent
	Errs   <-chan error
	Cancel context.CancelFunc
}
