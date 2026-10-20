// Package frontend is the frontend port: the ONE UI-neutral surface every
// frontend consumes. The terminal UI (the adapters/cli/tui split view) is the
// first frontend; a VS Code bridge and an ACP server are
// expected. Every frontend gets everything from this port, and core never
// knows which one is attached.
//
// The port is ConsumerService's Go face, grown rather than paralleled: where
// an RPC exists its method carries the RPC's name and meaning (ListRuns,
// WatchRuns, CredentialHolds), and the in-process implementation
// (operations.NewFrontendPort) projects the same coordinator state the wire
// does. What the port adds beyond observation — answering approvals,
// control verbs, the interactive terminal — is in-process only today: it is
// not on ConsumerService, whose consumer credential is read-only by design.
//
// The port's messages are core's own types (coord.RunInfo,
// coord.PendingApproval, coord.CredentialHold, agent.SessionEntry) and the few
// declared here. No frontend type — a style, a geometry, a toolkit message —
// appears in it; the layering rule core-and-shared-import-no-frontend keeps
// core free of every frontend package and of the Charm toolkit.
package frontend

import (
	"context"
	"errors"
	"io"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// Port is everything a frontend can observe and do.
//
// A session that hosts no coordinator (its startup failed or was skipped)
// still has a port: ListSessions and WatchFeed answer from the session store,
// ListRuns and CredentialHolds are empty, Approvals is nil, WatchRuns refuses
// with ErrNoCoordinator, and Control refuses with coord.ErrNotInjectable — the
// verb's own "no such child" refusal.
type Port interface {
	// ListSessions is the project's session index: every session recorded
	// for it, newest first, each with the liveness its session lock reports.
	// It is the roster's backdrop; the coordinator's runs enrich it.
	ListSessions(ctx context.Context) ([]Session, error)

	// ListRuns is ConsumerService.ListRuns: every run the coordinator holds,
	// ended ones included — each with its harp, agent, parent harp, state,
	// last activity, hold, terminal cause, unread mail and context usage.
	ListRuns(ctx context.Context) (coord.RunsSnapshot, error)

	// WatchRuns is ConsumerService.WatchRuns: the roster as one snapshot,
	// then every live event of the runs in runIDs (every run when empty)
	// from the instant of the snapshot forward, until ctx ends — when Events
	// closes. A subscriber too slow for the stream loses events, never
	// silently: an EventsLost marker takes their place.
	WatchRuns(ctx context.Context, runIDs []string) (RunWatch, error)

	// WatchFeed opens one agent's feed: its live event stream while the
	// coordinator holds it, else its transcript's tail. It runs until ctx
	// ends; Events closes when the feed does.
	WatchFeed(ctx context.Context, harp string) (Feed, error)

	// CredentialHolds is ConsumerService.CredentialHolds: every hold a turn
	// failure opened that is still in force (a rate limit, a refused
	// credential, an overload or unreachable endpoint backing one run off),
	// oldest first. A pause is not one; it shows as its run's Hold.
	CredentialHolds(ctx context.Context) ([]coord.CredentialHold, error)

	// Approvals is the root's approval queue: the requests parked for the
	// human, the subscription that announces each arrival and resolution,
	// the one place a request is answered (allow once, allow for the
	// session with rules, a posture change, deny with a message), and the
	// session grants an allow left, which the human may revoke. Nil when
	// the session hosts no queue.
	Approvals() coord.ApprovalSource

	// Control runs one control verb as the HUMAN initiator on any run the
	// coordinator holds: steer (the human's send — durable mail the target
	// takes at its next turn; with Interrupt, its turn in flight is cut and
	// the steer is its next turn now), question, summarize, pause, resume.
	Control(ctx context.Context, req coord.ControlRequest) (coord.ControlResult, error)

	// Terminal attaches to harp's interactive terminal: the session's own
	// interactive run, the one run with a pty. Every other run is headless
	// (a delegated child is watched through its feed), and so is a session
	// launched without a terminal: both are ErrNoTerminal.
	Terminal(harp string) (Terminal, error)
}

// The session liveness vocabulary: a session lock's three verdicts, one each.
// There is no fourth, and none is a guess.
const (
	SessionLive    = "live"
	SessionEnded   = "ended"
	SessionUnknown = "unknown"
)

// Session is one entry of the project's session index.
type Session struct {
	Harp string
	// Engine is the engine the session runs on, as the index records it.
	Engine string
	// State is SessionLive, SessionEnded or SessionUnknown.
	State string
}

// RunWatch is one open WatchRuns stream.
type RunWatch struct {
	Snapshot coord.RunsSnapshot
	Events   <-chan coord.Event
}

// Feed is one open agent feed.
type Feed struct {
	// Source is what feeds it: "live" (the coordinator's event stream) or
	// "store" (the transcript's tail).
	Source string
	Events <-chan FeedEvent
	// Errs (buffered) carries the error that ended the feed, if any, once
	// Events has closed.
	Errs <-chan error
}

// FeedEvent is one item of a feed: a transcript entry, or a gap — events
// lost between two that arrived, which a frontend shows rather than hides.
type FeedEvent struct {
	Entry *agent.SessionEntry
	Gap   int
}

// Terminal is an attached interactive terminal: reading yields the engine's
// output, writing types into it, and Resize sets the window size the engine
// sees. Reading ends (an error) once the engine's terminal is gone.
type Terminal interface {
	io.Reader
	io.Writer
	Resize(rows, cols uint16) error
}

// ErrNoTerminal refuses a Terminal attach to a run that has none.
var ErrNoTerminal = errors.New("frontend: this run has no interactive terminal — only the session's own interactive run does; a delegated child is headless, so watch its feed")

// ErrNoCoordinator refuses a coordinator-backed request in a session that
// hosts no coordinator.
var ErrNoCoordinator = errors.New("frontend: this session hosts no coordinator")
