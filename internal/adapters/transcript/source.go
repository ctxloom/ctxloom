package transcript

import (
	"context"

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
