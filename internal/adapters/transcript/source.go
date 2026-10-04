package transcript

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// Source is the host's read view of an agent's transcripts: materialize a
// session by id, list the store, or fetch the most-recent. Consumers (memory
// CLI, MCP load, compactor) depend on this rather than a concrete reader, so
// the canonical transcript and its compositions serve one contract.
// *CanonicalHistory, *FilteredSource and *CanonicalFallbackSource implement
// it.
type Source interface {
	GetSession(ctx context.Context, sessionID string) (*agent.Session, error)
	ListSessions(ctx context.Context) ([]agent.SessionMeta, error)
	CurrentSession(ctx context.Context) (*agent.Session, error)
}

// PlansSource fetches a session's plan documents by harp. Separate from
// Source (transcript reads) so consumers that only need plans — the
// compactor — depend on just this.
type PlansSource interface {
	GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error)
}

// Watcher opens a live structured turn stream over a session. Kept separate
// from Source (one-shot transcript reads) so a Source is not forced to
// implement a long-lived stream.
type Watcher interface {
	WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error)
}

// ErrNoHistory refuses a reader over an engine that keeps no readable store
// of its own: its transcripts are ctxloom's canonical capture alone.
var ErrNoHistory = errors.New("transcript: the engine has no session history of its own")

// NoSessionError reports a session id the engine's store does not hold.
type NoSessionError struct{ ID string }

func (e *NoSessionError) Error() string { return "no session " + e.ID + " in the engine's store" }
