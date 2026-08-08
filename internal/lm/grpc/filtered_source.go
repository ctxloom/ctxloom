package grpc

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript/policy"
)

// FilteredSource applies a content policy to every session it reads, on the
// way OUT of storage.
//
// READ side, deliberately, and this placement is the whole design:
//
//   - Storage stays TOTAL. The adapters canonicalize a vendor format without
//     judgement and the recorder writes what it is handed, so the canonical
//     transcript on disk is the complete record. Filtering is a VIEW over it,
//     not an edit to it.
//   - Therefore the policy is reversible. Change Default() and every existing
//     transcript is re-read under the new rules — no migration, no
//     re-materialization, nothing lost in the meantime.
//   - A write-side filter cannot make that promise. It was tried and reverted
//     (see NewRecorder): it survives only while every transcript is
//     re-derivable from a vendor log, which is true for tier-1 engines and
//     FALSE for a tier-2 raw-stream source, whose bytes are gone the moment
//     they are transformed. There, a write-time filter is permanent data
//     loss.
//
// It decorates SessionSource rather than living inside any one reader so the
// canonical reader, the legacy reader and the fallback composition all get the
// same policy from one wrap, and so a caller that genuinely wants the total
// record can simply not wrap.
type FilteredSource struct {
	inner  SessionSource
	policy policy.Policy
}

// NewFilteredSource wraps inner so every session it returns is filtered by p.
func NewFilteredSource(inner SessionSource, p policy.Policy) *FilteredSource {
	return &FilteredSource{inner: inner, policy: p}
}

var _ SessionSource = (*FilteredSource)(nil)

func (f *FilteredSource) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	sess, err := f.inner.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return f.policy.ApplySession(sess), nil
}

func (f *FilteredSource) CurrentSession(ctx context.Context) (*agent.Session, error) {
	sess, err := f.inner.CurrentSession(ctx)
	if err != nil {
		return nil, err
	}
	return f.policy.ApplySession(sess), nil
}

// ListSessions passes through untouched: SessionMeta carries counts and
// timestamps, never tool content, so there is nothing here for a content
// policy to act on. Filtering it would be a no-op dressed up as a decision.
func (f *FilteredSource) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	return f.inner.ListSessions(ctx)
}
