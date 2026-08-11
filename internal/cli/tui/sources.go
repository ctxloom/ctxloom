package tui

import (
	"context"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/operations"
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
	// ExportDir returns (creating if needed) the directory transcript
	// exports land in for harp — the harp's ctxloom session dir.
	ExportDir func(harp string) (string, error)
	// Inject delivers user-typed text into harp through the serving
	// coordinator, returning the delivery mode it reports (coord.Delivery*,
	// internal/agentcoord/coord). Nil when no coordinator is hosted.
	Inject func(harp, text string) (string, error)
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
	// State is the roster vocabulary: executing | queued | parked | idle |
	// ended for orchestrator-held children; live | ended for index sessions.
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
