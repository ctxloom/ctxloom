package tui

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// RosterRow is one line of the agents pane: the port's roster row.
type RosterRow = frontend.RosterRow

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
	return frontend.BuildRoster(index, held, self), nil
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
