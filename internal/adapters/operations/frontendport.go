package operations

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// FrontendDeps compose the in-process frontend port of one session.
type FrontendDeps struct {
	// Coord is the session's hosted coordinator; nil when none is hosted.
	Coord *coord.Coordinator
	// Engines resolves a session's engine for its feed.
	Engines engine.Registry
	// WorkDir is the project whose sessions the port lists.
	WorkDir string
	// Self is the session's own harp: the one run Terminal attaches to.
	Self string
	// Terminal is the session's own interactive terminal — the pty its
	// engine runs on; nil for a session launched without one.
	Terminal TerminalHost
	// SessionState is the liveness probe ListSessions asks; nil is
	// LockSessionState.
	SessionState SessionStateProbe
}

// TerminalHost is the pty an interactive run's engine is on, as the frontend
// port attaches to it: its master (read the engine's output, write its
// input) and its window size. hostpty.Session is one.
type TerminalHost interface {
	Master() io.ReadWriter
	Resize(rows, cols uint16) error
}

// NewFrontendPort is the in-process frontend port: ConsumerService's
// projections read straight off the hosted coordinator, the session index
// and feeds from the session store, and the session's own pty as its
// terminal.
func NewFrontendPort(d FrontendDeps) frontend.Port {
	if d.SessionState == nil {
		d.SessionState = LockSessionState
	}
	return &frontendPort{d: d}
}

type frontendPort struct{ d FrontendDeps }

func (p *frontendPort) ListSessions(context.Context) ([]frontend.Session, error) {
	entries, err := ListSessionsForProject(p.d.WorkDir)
	if err != nil {
		return nil, err
	}
	out := make([]frontend.Session, len(entries))
	for i, e := range entries {
		out[i] = frontend.Session{Harp: e.HarpName, Engine: e.Backend, State: p.d.SessionState(e.HarpName), LastActivity: e.LastActivity}
	}
	return out, nil
}

func (p *frontendPort) ListRuns(context.Context) (coord.RunsSnapshot, error) {
	if p.d.Coord == nil {
		return coord.RunsSnapshot{}, nil
	}
	return p.d.Coord.ListRuns(true, ""), nil
}

func (p *frontendPort) WatchRuns(ctx context.Context, runIDs []string) (frontend.RunWatch, error) {
	if p.d.Coord == nil {
		return frontend.RunWatch{}, frontend.ErrNoCoordinator
	}
	snap, events, cancel, _ := p.d.Coord.WatchRuns(runIDs)
	out := make(chan coord.Event)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case ev := <-events:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return frontend.RunWatch{Snapshot: snap, Events: out}, nil
}

func (p *frontendPort) WatchFeed(ctx context.Context, harp string) (frontend.Feed, error) {
	feed, err := WatchSessionFeed(ctx, p.d.Engines, SessionFeedRequest{Harp: harp})
	if err != nil {
		return frontend.Feed{}, err
	}
	out := make(chan frontend.FeedEvent)
	go func() {
		defer close(out)
		for ev := range feed.Events {
			fe, shown := frontendFeedEvent(ev)
			if !shown {
				continue
			}
			select {
			case out <- fe:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frontend.Feed{Source: feed.Source, Events: out, Errs: feed.Errs}, nil
}

// frontendFeedEvent is ev as the port's feed carries it, and whether it
// carries anything: a response boundary or a heartbeat marks the stream, it
// is not an item a frontend shows.
func frontendFeedEvent(ev SessionFeedEvent) (frontend.FeedEvent, bool) {
	fe := frontend.FeedEvent{Gap: ev.Gap}
	if ev.Event != nil {
		fe.Entry = ev.Event.Entry
	}
	return fe, fe.Entry != nil || fe.Gap > 0
}

func (p *frontendPort) Roster(ctx context.Context, q frontend.RosterQuery) (frontend.Roster, error) {
	return frontend.ComposeRoster(ctx, p, q)
}

func (p *frontendPort) CredentialHolds(context.Context) ([]coord.CredentialHold, error) {
	if p.d.Coord == nil {
		return nil, nil
	}
	return p.d.Coord.CredentialHolds(), nil
}

func (p *frontendPort) Approvals() coord.ApprovalSource {
	// A nil *ApprovalQueue inside the interface would read as a source to
	// everything that checks it.
	if p.d.Coord == nil || p.d.Coord.Approvals() == nil {
		return nil
	}
	return p.d.Coord.Approvals()
}

func (p *frontendPort) Control(ctx context.Context, req coord.ControlRequest) (coord.ControlResult, error) {
	if p.d.Coord == nil {
		return coord.ControlResult{}, coord.ErrNotInjectable
	}
	return p.d.Coord.Control(ctx, coord.ControlInitiator{Kind: coord.InitiatorHuman}, req)
}

func (p *frontendPort) Terminal(harp string) (frontend.Terminal, error) {
	if p.d.Terminal == nil || harp != p.d.Self {
		return nil, frontend.ErrNoTerminal
	}
	return hostTerminal{p.d.Terminal}, nil
}

// hostTerminal is a TerminalHost as an attached frontend.Terminal.
type hostTerminal struct{ h TerminalHost }

func (t hostTerminal) Read(b []byte) (int, error)     { return t.h.Master().Read(b) }
func (t hostTerminal) Write(b []byte) (int, error)    { return t.h.Master().Write(b) }
func (t hostTerminal) Resize(rows, cols uint16) error { return t.h.Resize(rows, cols) }
