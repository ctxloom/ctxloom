package tui

import (
	"context"
	"time"

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
	// Approvals is the root's approval queue: what the approvals view lists
	// and the only place it answers. Nil when no coordinator is hosted; the
	// view then says so rather than opening.
	Approvals coord.ApprovalSource
	// Now is the view's clock for countdowns and tombstones; nil is real time.
	Now func() time.Time
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
