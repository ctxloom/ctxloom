package tui

import (
	"context"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

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
	// Hold is the hold parking the child's run (nil when none does). A held
	// run's State stays idle, so the hold is what tells it apart.
	Hold *coord.RunHold
	// Cause and Detail are why an ended child's run ended, and the detail
	// that came with it: the run's, empty until the run ends.
	Cause  string
	Detail string
	// Unread is how many messages wait undelivered in the child's inbox
	// (coord.RunInfo.Unread).
	Unread int
	// Pending is how many approvals the agent waits on the human for: the
	// approval queue's requests whose asker (From) is this harp. The split
	// view sets it; BuildRoster leaves it zero.
	Pending int
	// LastActivity is when the coordinator last saw the run change state
	// (coord.RunInfo.LastEventAt); zero for a row it holds no run for.
	LastActivity time.Time
	// SessionActivity is when the session index last saw the session worked
	// (frontend.Session.LastActivity); zero for a run the index lacks.
	SessionActivity time.Time
	// ContextTokens of ContextWindow is how full the agent's context is, as
	// its engine last reported (coord.RunInfo); zero when nothing has.
	ContextTokens uint64
	ContextWindow uint64
}

// Activity is when r was last active: the later of the coordinator's last
// event and the index's last-worked time. The tree is ordered by it.
func (r RosterRow) Activity() time.Time {
	if r.LastActivity.After(r.SessionActivity) {
		return r.LastActivity
	}
	return r.SessionActivity
}

// Feed is one open observation feed. Cancel releases the watch (switching
// harps, closing the split view).
type Feed struct {
	// Source is what actually fed the stream: "live" or "store".
	Source string
	Events <-chan frontend.FeedEvent
	Errs   <-chan error
	Cancel context.CancelFunc
}

// LoadRoster is the split view's roster over port: the session index merged
// with the coordinator's runs (BuildRoster), self pinned on top. The runs are
// enrichment — children's lineage and state — and their absence never blanks
// the pane: a port that cannot list them leaves the index rows as they are.
func LoadRoster(ctx context.Context, port frontend.Port, self string) ([]RosterRow, error) {
	if port == nil {
		return nil, nil
	}
	index, err := port.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	var held []coord.RunInfo
	if runs, err := port.ListRuns(ctx); err == nil {
		held = runs.Runs
	}
	return BuildRoster(index, held, self), nil
}

// openFeed opens harp's feed over port, scoped to a context the returned
// Feed's Cancel ends.
func openFeed(ctx context.Context, port frontend.Port, harp string) (*Feed, error) {
	if port == nil {
		return nil, frontend.ErrNoCoordinator
	}
	wctx, cancel := context.WithCancel(ctx)
	f, err := port.WatchFeed(wctx, harp)
	if err != nil {
		cancel()
		return nil, err
	}
	return &Feed{Source: f.Source, Events: f.Events, Errs: f.Errs, Cancel: cancel}, nil
}
