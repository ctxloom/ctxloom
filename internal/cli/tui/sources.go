package tui

import (
	"context"
	"time"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// Sources are the overlay's data seams. The CLI wires them to the operations
// feed resolver, the session index, and the agent-bus roster; tests inject
// fakes.
type Sources struct {
	// Roster lists the observable sessions (index sessions merged with
	// orchestrator-held children — see BuildRoster).
	Roster func(ctx context.Context) ([]RosterRow, error)
	// Watch opens a harp's observation feed (operations.WatchSessionFeed
	// behind a cancel).
	Watch func(ctx context.Context, harp string) (*Feed, error)
	// ExportDir returns (creating if needed) the directory transcript
	// exports land in for harp — the harp's ctxloom session dir.
	ExportDir func(harp string) (string, error)
	// Now is the export-filename clock; nil means time.Now.
	Now func() time.Time
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
	Self   bool
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
