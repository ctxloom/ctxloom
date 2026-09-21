package transcript

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// Source is the host's read view of an agent's transcripts: materialize a
// session by id, list the store, or fetch the most-recent. Consumers (memory
// CLI, MCP load, compactor) depend on this rather than a concrete reader, so
// the canonical transcript, an engine's own store and the fallback
// composition serve one contract. *CanonicalHistory, *EngineReader,
// *FilteredSource and *CanonicalFallbackSource implement it.
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
// from Source (one-shot transcript reads) so the in-process adapters that
// satisfy Source — the compactor's memoryHistorySource — are not forced to
// implement a long-lived stream they never use; only `session transcript
// watch` depends on this.
type Watcher interface {
	WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error)
}

// ErrNoHistory refuses a reader over an engine that keeps no readable store
// of its own: its transcripts are ctxloom's canonical capture alone.
var ErrNoHistory = errors.New("transcript: the engine has no session history of its own")

// EngineReader reads an engine's OWN transcript store in-process: the
// engine's agent.SessionHistory — which locates, reassembles and
// translates its native transcripts — over the project the reader is
// scoped to. It is the legacy leg CanonicalFallbackSource falls back onto
// for a harp that predates canonical capture; an engine that retired its
// scraper has none (operations.HistoryForBackend refuses), and its callers
// pass no legacy leg at all.
type EngineReader struct {
	hist    agent.SessionHistory
	workDir string
}

var (
	_ Source      = (*EngineReader)(nil)
	_ PlansSource = (*EngineReader)(nil)
	_ Watcher     = (*EngineReader)(nil)
)

// NewEngineReader returns a reader over hist scoped to workDir (the project
// whose store is read). A nil hist is refused at every read (ErrNoHistory)
// rather than dereferenced.
func NewEngineReader(hist agent.SessionHistory, workDir string) *EngineReader {
	return &EngineReader{hist: hist, workDir: workDir}
}

// GetSession materializes the transcript for sessionID into the normalized
// form. "No such session" is an error naming the id, never a nil session:
// a nil, error-free session is indistinguishable from one that exists and
// has produced no turns yet.
func (r *EngineReader) GetSession(_ context.Context, sessionID string) (*agent.Session, error) {
	if r.hist == nil {
		return nil, ErrNoHistory
	}
	sess, err := r.hist.GetSession(r.workDir, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, &NoSessionError{ID: sessionID}
	}
	return sess, nil
}

// NoSessionError reports a session id the engine's store does not hold.
type NoSessionError struct{ ID string }

func (e *NoSessionError) Error() string { return "no session " + e.ID + " in the engine's store" }

// WatchSession opens a live structured turn stream for sessionID: the
// store is polled and diffed against a high-water mark (sessionWatcher).
func (r *EngineReader) WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error) {
	if r.hist == nil {
		return nil, nil, ErrNoHistory
	}
	events, errs := pollTranscript(ctx, "watch session", sessionID, 0, func() (*agent.Session, error) {
		return r.hist.GetSession(r.workDir, sessionID)
	})
	return events, errs, nil
}

// ListSessions returns the engine's transcript-store metadata, most-recent-
// first (the engine's own ordering).
func (r *EngineReader) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	if r.hist == nil {
		return nil, ErrNoHistory
	}
	return r.hist.ListSessions(r.workDir)
}

// GetPlans reads a harp's plan documents from its ctxloom session dir.
func (r *EngineReader) GetPlans(_ context.Context, harp string) ([]agent.PlanFile, error) {
	return ReadPlanFiles(harp), nil
}

// CurrentSession materializes the engine's most-recent transcript: the
// most-recent entry it lists, fetched by id. Returns a nil session and nil
// error when the store is empty, so callers can present a clean "no
// sessions" message.
func (r *EngineReader) CurrentSession(ctx context.Context) (*agent.Session, error) {
	metas, err := r.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	if len(metas) == 0 {
		return nil, nil
	}
	return r.GetSession(ctx, metas[0].ID)
}
