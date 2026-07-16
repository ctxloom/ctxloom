package grpc

import (
	"context"
	"sort"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// This file is the tough-cloud S4 consumer cutover: CanonicalFallbackSource is
// the transitional pb.SessionSource every production reader (compactor, MCP
// memory tools, `memory`/`session` CLI commands) is flipped onto. It prefers
// ctxloom's own captured transcript.acp.jsonl (transcript.CanonicalHistory,
// S3) for any harp that has one, and falls back to the legacy per-engine
// SessionSource (typically *SessionReader, going over gRPC to the backend's
// own scraper) only for a harp that predates capture — plan §4b "Selection
// rule (transitional)". New sessions always have a canonical transcript (S2
// tees every structured Chat), so the fallback decays to zero over time; it
// is deliberately NOT removed here (that is S5's job).
//
// This type lives in internal/lm/grpc, not internal/transcript, because
// CanonicalHistory's own package doc forbids importing this package (would
// create transcript -> grpc -> transcript, since chat.go already imports
// transcript for Tee) — but the reverse direction is fine, and pb.SessionSource
// is defined here anyway.

// CanonicalFallbackSource wraps a legacy SessionSource with canonical-first
// selection. Store resolves a backend-native session id to the harp that owns
// it (the reverse of the index's forward SessionID lookup) so GetSession's
// sessionID parameter — always backend-native at every call site in this
// codebase — can still be checked against the canonical store, which is
// harp-keyed.
type CanonicalFallbackSource struct {
	legacy    SessionSource
	canonical *transcript.CanonicalHistory
	store     sessions.Store
}

var _ SessionSource = (*CanonicalFallbackSource)(nil)

// NewCanonicalFallbackSource returns a CanonicalFallbackSource scoped to
// workDir (the project the canonical enumeration/CurrentSession is limited
// to, matching legacy's own self-situated project scoping).
func NewCanonicalFallbackSource(legacy SessionSource, workDir string, store sessions.Store) *CanonicalFallbackSource {
	return &CanonicalFallbackSource{
		legacy:    legacy,
		canonical: transcript.NewCanonicalHistory(workDir, store),
		store:     store,
	}
}

// harpForSessionID reverse-resolves a backend-native session id to its owning
// harp via the index, or "" when unbound/unknown. A best-effort, read-only
// lookup: an index error degrades to "" (legacy fallback), never an error —
// selection must never block a read the legacy path could still serve.
func (f *CanonicalFallbackSource) harpForSessionID(sessionID string) string {
	if sessionID == "" || f.store == nil {
		return ""
	}
	idx, err := f.store.Load()
	if err != nil {
		return ""
	}
	for _, e := range idx.Sessions {
		if e.SessionID == sessionID {
			return e.HarpName
		}
	}
	return ""
}

// GetSession resolves sessionID (backend-native) to its harp and prefers the
// canonical transcript when that harp has one captured; otherwise (no bound
// harp, or the harp predates capture) falls back to the legacy source keyed
// directly by sessionID, unchanged from pre-S4 behavior.
func (f *CanonicalFallbackSource) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	if harp := f.harpForSessionID(sessionID); harp != "" {
		if sess, err := f.canonical.GetSession(ctx, harp); err == nil {
			return sess, nil
		}
		// No canonical transcript (or it failed to parse) for this harp: fall
		// through to the legacy read below rather than surfacing the
		// canonical-side error, since the legacy transcript may still be
		// perfectly readable (the whole point of a transitional fallback).
	}
	return f.legacy.GetSession(ctx, sessionID)
}

// CurrentSession prefers the project's most-recently-active canonical-backed
// session; falls back to the legacy source's own notion of "current" when the
// project has none (pre-capture project, or every session in it predates
// capture).
func (f *CanonicalFallbackSource) CurrentSession(ctx context.Context) (*agent.Session, error) {
	sess, err := f.canonical.CurrentSession(ctx)
	if err == nil && sess != nil {
		return sess, nil
	}
	return f.legacy.CurrentSession(ctx)
}

// ListSessions merges canonical-backed sessions for this project with legacy
// entries not already covered by one of those harps (deduped by backend
// session id, so a harp with BOTH a canonical transcript and a legacy
// transcript file is listed once, from canonical). Best-effort: a canonical
// listing failure degrades to legacy-only rather than erroring the whole
// list.
func (f *CanonicalFallbackSource) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	canonMetas, _ := f.canonical.ListSessions(ctx)

	covered := make(map[string]bool, len(canonMetas))
	if f.store != nil {
		for _, m := range canonMetas {
			if entry, _ := f.store.Find(m.ID); entry != nil && entry.SessionID != "" {
				covered[entry.SessionID] = true
			}
		}
	}

	legacyMetas, err := f.legacy.ListSessions(ctx)
	if err != nil {
		if len(canonMetas) == 0 {
			return nil, err
		}
		// Canonical still has something to show even though legacy failed —
		// degrade to canonical-only rather than losing the whole listing.
		legacyMetas = nil
	}

	out := make([]agent.SessionMeta, 0, len(canonMetas)+len(legacyMetas))
	out = append(out, canonMetas...)
	for _, m := range legacyMetas {
		if !covered[m.ID] {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartTime.After(out[j].StartTime)
	})
	return out, nil
}
