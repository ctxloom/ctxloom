// Package frontendfake is a frontend.Port double for a frontend's unit tests:
// every method is a field the test sets, and a method whose field is unset
// answers as a session that hosts nothing — no sessions, no runs, no holds,
// no approval queue, control refused as for a harp no coordinator holds, no
// terminal. A test that needs the port's behaviour against a live session
// uses the contract's World (frontendcontract) instead.
package frontendfake

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// Port is the double.
type Port struct {
	ListSessionsFn    func(ctx context.Context) ([]frontend.Session, error)
	ListRunsFn        func(ctx context.Context) (coord.RunsSnapshot, error)
	WatchRunsFn       func(ctx context.Context, runIDs []string) (frontend.RunWatch, error)
	WatchFeedFn       func(ctx context.Context, harp string) (frontend.Feed, error)
	CredentialHoldsFn func(ctx context.Context) ([]coord.CredentialHold, error)
	// ApprovalSource is what Approvals returns.
	ApprovalSource coord.ApprovalSource
	ControlFn      func(ctx context.Context, req coord.ControlRequest) (coord.ControlResult, error)
	TerminalFn     func(harp string) (frontend.Terminal, error)
	// RosterFn answers Roster; unset, Roster composes the double's own
	// sessions, runs and approvals (frontend.ComposeRoster).
	RosterFn func(ctx context.Context, q frontend.RosterQuery) (frontend.Roster, error)
}

var _ frontend.Port = (*Port)(nil)

func (p *Port) ListSessions(ctx context.Context) ([]frontend.Session, error) {
	if p.ListSessionsFn == nil {
		return nil, nil
	}
	return p.ListSessionsFn(ctx)
}

func (p *Port) ListRuns(ctx context.Context) (coord.RunsSnapshot, error) {
	if p.ListRunsFn == nil {
		return coord.RunsSnapshot{}, nil
	}
	return p.ListRunsFn(ctx)
}

func (p *Port) WatchRuns(ctx context.Context, runIDs []string) (frontend.RunWatch, error) {
	if p.WatchRunsFn == nil {
		return frontend.RunWatch{}, frontend.ErrNoCoordinator
	}
	return p.WatchRunsFn(ctx, runIDs)
}

func (p *Port) WatchFeed(ctx context.Context, harp string) (frontend.Feed, error) {
	if p.WatchFeedFn == nil {
		events := make(chan frontend.FeedEvent)
		close(events)
		return frontend.Feed{Source: "store", Events: events, Errs: make(chan error, 1)}, nil
	}
	return p.WatchFeedFn(ctx, harp)
}

func (p *Port) Roster(ctx context.Context, q frontend.RosterQuery) (frontend.Roster, error) {
	if p.RosterFn == nil {
		return frontend.ComposeRoster(ctx, p, q)
	}
	return p.RosterFn(ctx, q)
}

func (p *Port) CredentialHolds(ctx context.Context) ([]coord.CredentialHold, error) {
	if p.CredentialHoldsFn == nil {
		return nil, nil
	}
	return p.CredentialHoldsFn(ctx)
}

func (p *Port) Approvals() coord.ApprovalSource { return p.ApprovalSource }

func (p *Port) Control(ctx context.Context, req coord.ControlRequest) (coord.ControlResult, error) {
	if p.ControlFn == nil {
		return coord.ControlResult{}, coord.ErrNotInjectable
	}
	return p.ControlFn(ctx, req)
}

func (p *Port) Terminal(harp string) (frontend.Terminal, error) {
	if p.TerminalFn == nil {
		return nil, frontend.ErrNoTerminal
	}
	return p.TerminalFn(harp)
}
