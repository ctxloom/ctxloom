package grpc

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// SessionReader is the host-side consumer of the agent-authoritative transcript
// contract. It retrieves normalized transcripts from a backend's plugin server
// over gRPC instead of parsing the backend's files in-process — so the same code
// path works when the agent is remote. Each operation spawns a short-lived,
// self-situated `ctxloom llm serve <backend>` plugin (the agent resolves its own
// workspace), invokes the RPC, and tears it down.
//
// No workspace/path is passed: the locator is the agent-agnostic session id, and
// "which" session (current/previous/ordering) is ctxloom's call via its index —
// the reader only materializes a given id (or lists the agent's store).
type SessionReader struct {
	backendName string
	verbosity   int
	factory     ClientFactory
}

// SessionSource is the host's read view of an agent's transcripts: materialize a
// session by id, list the store, or fetch the most-recent. Consumers (memory
// CLI, MCP load, compactor, resume picker) depend on this rather than an
// in-process SessionHistory, so the same code path serves a remote agent.
// *SessionReader is the production implementation (over gRPC).
type SessionSource interface {
	GetSession(ctx context.Context, sessionID string) (*agent.Session, error)
	ListSessions(ctx context.Context) ([]agent.SessionMeta, error)
	CurrentSession(ctx context.Context) (*agent.Session, error)
}

// PlansSource fetches a session's plan documents by harp. Separate from
// SessionSource (transcript reads) so consumers that only need plans — the
// compactor — depend on just this. *SessionReader implements both.
type PlansSource interface {
	GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error)
}

// SessionWatcher opens a live structured turn stream over a session. Kept
// separate from SessionSource (one-shot transcript reads) so the in-process
// adapters that satisfy SessionSource — the compactor's memoryHistorySource —
// are not forced to implement a long-lived stream they never use; only the
// `session transcript watch` / structured-chat paths depend on this. *SessionReader
// implements it over gRPC, keeping the plugin alive for the stream's lifetime.
type SessionWatcher interface {
	WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error)
}

var (
	_ SessionSource  = (*SessionReader)(nil)
	_ PlansSource    = (*SessionReader)(nil)
	_ SessionWatcher = (*SessionReader)(nil)
)

// NewSessionReader returns a reader for the named backend using the default
// self-invoking plugin client.
func NewSessionReader(backendName string, verbosity int) *SessionReader {
	return &SessionReader{
		backendName: backendName,
		verbosity:   verbosity,
		factory:     DefaultClientFactory(),
	}
}

// NewSessionReaderWithFactory injects a client factory (tests use a mock so they
// never spawn a real subprocess).
func NewSessionReaderWithFactory(backendName string, verbosity int, factory ClientFactory) *SessionReader {
	return &SessionReader{backendName: backendName, verbosity: verbosity, factory: factory}
}

// withClient dials a plugin, runs fn against it, and always tears the plugin
// down afterward.
func (r *SessionReader) withClient(fn func(Client) error) error {
	c, err := r.factory(r.backendName, "", r.verbosity)
	if err != nil {
		return fmt.Errorf("start %s plugin: %w", r.backendName, err)
	}
	defer c.Kill()
	return fn(c)
}

// GetSession materializes the transcript for sessionID into the normalized form.
func (r *SessionReader) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	var out *agent.Session
	err := r.withClient(func(c Client) error {
		s, e := c.GetSession(ctx, sessionID)
		out = s
		return e
	})
	return out, err
}

// WatchSession opens a live structured turn stream for sessionID. Unlike the
// other reads, the plugin must outlive the call — the stream is long-lived — so
// this does NOT use withClient (which tears the plugin down on return). Instead
// it dials, starts the stream, and kills the plugin only once the stream ends
// (the events channel drains) or the context is cancelled. A fatal mid-stream
// error is forwarded on the returned errors channel.
func (r *SessionReader) WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error) {
	c, err := r.factory(r.backendName, "", r.verbosity)
	if err != nil {
		return nil, nil, fmt.Errorf("start %s plugin: %w", r.backendName, err)
	}

	events, errs, err := c.WatchSession(ctx, sessionID)
	if err != nil {
		c.Kill()
		return nil, nil, err
	}

	// Bind plugin lifetime to stream lifetime: forward events through, then tear
	// the plugin down once the upstream stream closes. The forwarding goroutine
	// is the sole owner of the plugin handle after this point.
	outEvents := make(chan *WatchEvent)
	outErrs := make(chan error, 1)
	go func() {
		defer c.Kill()
		defer close(outEvents)
		defer close(outErrs)
		for ev := range events {
			select {
			case outEvents <- ev:
			case <-ctx.Done():
				return
			}
		}
		if e := <-errs; e != nil {
			outErrs <- e
		}
	}()
	return outEvents, outErrs, nil
}

// ListSessions returns the backend's transcript-store metadata, most-recent-first
// (the agent's own ordering).
func (r *SessionReader) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	var out []agent.SessionMeta
	err := r.withClient(func(c Client) error {
		m, e := c.ListSessions(ctx)
		out = m
		return e
	})
	return out, err
}

// GetPlans fetches a harp's plan documents from the agent server.
func (r *SessionReader) GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error) {
	var out []agent.PlanFile
	err := r.withClient(func(c Client) error {
		p, e := c.GetPlans(ctx, harp)
		out = p
		return e
	})
	return out, err
}

// CurrentSession materializes the backend's most-recent transcript. "Current" is
// resolved host-side (the most-recent entry the agent lists) and then fetched by
// id, in a single plugin lifetime. Returns a nil session and nil error when the
// store is empty, so callers can present a clean "no sessions" message.
func (r *SessionReader) CurrentSession(ctx context.Context) (*agent.Session, error) {
	var out *agent.Session
	err := r.withClient(func(c Client) error {
		metas, e := c.ListSessions(ctx)
		if e != nil {
			return e
		}
		if len(metas) == 0 {
			return nil
		}
		s, e := c.GetSession(ctx, metas[0].ID)
		out = s
		return e
	})
	return out, err
}
