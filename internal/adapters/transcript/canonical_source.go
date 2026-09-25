package transcript

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// CanonicalFallbackSource is the Source every production reader (compactor,
// MCP memory tools, `memory`/`session` CLI commands) reads through. It
// prefers ctxloom's own captured transcript.jsonl (CanonicalHistory) for any
// harp that has one, and falls back to a legacy per-engine Source (an
// EngineReader over the engine's own store) only for a harp that predates
// capture. Every new session has a canonical transcript (the runner records
// every structured turn), so the fallback decays to zero over time.
//
// An engine that retired its own scraper DECLARES that on its descriptor
// (hosting.Hosting.NoLegacyHistoryReason; read through
// backends.NoLegacyHistoryReason), and a caller building a source for it
// passes legacy=nil: canonical capture is the ONLY source, with no legacy
// leg to ever fall back to. Every other engine keeps its legacy leg.

// CanonicalFallbackSource wraps a legacy Source with canonical-first
// selection. Store resolves a backend-native session id to the harp that owns
// it (the reverse of the index's forward SessionID lookup) so GetSession's
// sessionID parameter — always backend-native at every call site in this
// codebase — can still be checked against the canonical store, which is
// harp-keyed.
//
// legacy may be nil: for a retired-scraper backend there is no
// legacy reader to fall back to at all, so every method serves canonical-only
// and degrades to canonical's own "not found"/"empty" contract instead of
// ever dereferencing legacy.
type CanonicalFallbackSource struct {
	legacy    Source // nil for a retired-scraper backend: canonical-only
	canonical *CanonicalHistory
	store     sessions.Store
}

var _ Source = (*CanonicalFallbackSource)(nil)

// NewCanonicalFallbackSource returns a CanonicalFallbackSource scoped to
// workDir (the project the canonical enumeration/CurrentSession is limited
// to, matching legacy's own project scoping). Pass legacy=nil for a
// retired-scraper backend — canonical becomes the sole source, never
// falling back.
func NewCanonicalFallbackSource(legacy Source, workDir string, store sessions.Store) *CanonicalFallbackSource {
	return &CanonicalFallbackSource{
		legacy:    legacy,
		canonical: NewCanonicalHistory(workDir, store),
		store:     store,
	}
}

// harpForSessionID reverse-resolves a backend-native session id to its owning
// harp via the index, or "" when unbound/unknown. A best-effort, read-only
// lookup: an index error degrades to "" (legacy fallback), never an error —
// selection must never block a read the legacy path could still serve.
//
// Store.FindBySessionID is the ONE definition of "which harp owns this id",
// and it is a LINEAGE lookup: it matches an id the harp is currently bound to
// AND any id a /clear rebind has displaced into Entry.Rotations. Both count.
// The id recover_session targets after a context wipe is precisely a
// rotated-away one — the pre-clear thread — and a scan of the current binding
// alone could not map it to its harp, so the read fell through to a legacy
// leg a retired-scraper backend does not have and the caller was told there
// was nothing to recover.
func (f *CanonicalFallbackSource) harpForSessionID(sessionID string) string {
	if sessionID == "" || f.store == nil {
		return ""
	}
	entry, err := f.store.FindBySessionID(sessionID)
	if err != nil || entry == nil {
		return ""
	}
	return entry.HarpName
}

// GetSession resolves id — which callers pass as EITHER a harp OR a
// backend-native session id, see below — and prefers the canonical
// transcript when one is captured; otherwise falls back to the legacy source
// keyed directly by id. When legacy is nil (a retired-scraper backend)
// there is nothing to fall back to: no
// canonical transcript for a resolvable harp, or an unresolvable id, is a
// genuine "no session" rather than a scrape attempt.
//
// id is resolved HARP-FIRST, not just as a backend-native
// session id. `memory list`'s SESSION ID column literally displays the harp
// for any canonical-backed session (CanonicalHistory.ListSessions sets
// meta.ID = harp — the sessionID field never surfaces to a user at all), so
// `memory show <that value>` must resolve it — the direct harp lookup
// session/distill already do successfully via the index, unlike the reverse
// sessionID->harp lookup below, which only ever matches a genuine
// backend-native id. A harp and a backend-native session id never collide in
// practice (harps are ctxloom's own three-word names; native ids are
// engine-specific UUIDs/opaque strings), so trying id-as-harp first costs
// only one cheap failed stat on the (far more common) sessionID-first path
// and changes nothing about which session an already-working call resolves
// to.
func (f *CanonicalFallbackSource) GetSession(ctx context.Context, id string) (*agent.Session, error) {
	sess, firstErr := f.canonical.GetSession(ctx, id)
	if firstErr == nil {
		return sess, nil
	}

	// "This harp captured no transcript" is the selection rule saying "try the
	// other leg", not a failure. Any OTHER canonical error — corrupt, truncated,
	// a schema this build refuses to guess at — is a real one, and reporting it
	// as absence tells the user to stop looking for a transcript that is right
	// there on disk.
	var lastErr error
	var uncaptured *NoCanonicalTranscriptError
	if !errors.As(firstErr, &uncaptured) {
		lastErr = firstErr
	}
	if harp := f.harpForSessionID(id); harp != "" {
		sess, err := f.canonical.GetSession(ctx, harp)
		if err == nil {
			// Resolving THROUGH the harp is an implementation detail of this
			// lookup, not a change of identity: the caller addressed the session
			// by id and must get that id back. Letting the harp leak out here
			// re-keys everything downstream — Compactor keys the essence it
			// writes off session.ID, so the caller then reads back under the id
			// it passed and finds nothing, and a correctly distilled essence is
			// silently unreadable the moment it is written.
			sess.ID = id
			return sess, nil
		}
		// No canonical transcript (or it failed to parse) for this harp: fall
		// through to the legacy read below rather than surfacing the
		// canonical-side error, since the legacy transcript may still be
		// perfectly readable (the whole point of a transitional fallback) —
		// unless legacy is nil, in which case this canonical-side error IS
		// the answer.
		lastErr = err
	}
	if f.legacy == nil {
		if lastErr != nil {
			return nil, lastErr
		}
		// Nothing better emerged, so the FIRST attempt's error is the answer.
		// It was set aside above as a selection signal ("try the other leg"),
		// not because it was uninformative — a NoCanonicalTranscriptError names
		// the harp and the concrete remedy (importing the vendor transcript),
		// and discarding it here replaced that with a generic message the
		// caller cannot act on.
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("no canonical transcript for session %q (legacy scraper reader retired)", id)
	}
	return f.legacy.GetSession(ctx, id)
}

// CurrentSession prefers the project's most-recently-active canonical-backed
// session; falls back to the legacy source's own notion of "current" when the
// project has none (pre-capture project, or every session in it predates
// capture) and a legacy source exists. When legacy is nil canonical's
// own "no sessions" contract (nil, nil) is returned as-is.
func (f *CanonicalFallbackSource) CurrentSession(ctx context.Context) (*agent.Session, error) {
	sess, err := f.canonical.CurrentSession(ctx)
	if err == nil && sess != nil {
		return sess, nil
	}
	if f.legacy == nil {
		return sess, err
	}
	return f.legacy.CurrentSession(ctx)
}

// ListSessions merges canonical-backed sessions for this project with legacy
// entries not already covered by one of those harps (deduped by backend
// session id, so a harp with BOTH a canonical transcript and a legacy
// transcript file is listed once, from canonical). Best-effort: a canonical
// listing failure degrades to legacy-only rather than erroring the whole
// list. When legacy is nil the listing is canonical-only.
func (f *CanonicalFallbackSource) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	canonMetas, canonErr := f.canonical.ListSessions(ctx)

	if f.legacy == nil {
		// No legacy leg to fall back to (a retired scraper, declared on the
		// engine's descriptor): a failed canonical read is the WHOLE
		// listing's failure, not "zero sessions". Discarding canonErr here
		// would report a confident empty list indistinguishable from a
		// project that genuinely has none.
		if canonErr != nil {
			return nil, canonErr
		}
		sortNewestFirst(canonMetas)
		return canonMetas, nil
	}

	covered := f.coveredSessionIDs(canonMetas)
	legacyMetas, err := f.legacyListing(ctx, canonMetas, canonErr)
	if err != nil {
		return nil, err
	}

	out := make([]agent.SessionMeta, 0, len(canonMetas)+len(legacyMetas))
	out = append(out, canonMetas...)
	for _, m := range legacyMetas {
		if !covered[m.ID] {
			out = append(out, m)
		}
	}
	sortNewestFirst(out)
	return out, nil
}

// sortNewestFirst orders sessions by start time, newest first, stably.
func sortNewestFirst(metas []agent.SessionMeta) {
	sort.SliceStable(metas, func(i, j int) bool {
		return metas[i].StartTime.After(metas[j].StartTime)
	})
}

// coveredSessionIDs is the backend session ids the canonical sessions'
// harps are bound to — the legacy entries already listed from canonical.
// One enumeration for the whole dedup set, not one Find per canonical
// session; an unreadable store simply covers nothing.
func (f *CanonicalFallbackSource) coveredSessionIDs(canonMetas []agent.SessionMeta) map[string]bool {
	covered := make(map[string]bool, len(canonMetas))
	if f.store == nil || len(canonMetas) == 0 {
		return covered
	}
	all, err := f.store.ListAll()
	if err != nil {
		return covered
	}
	sessionIDByHarp := make(map[string]string, len(all))
	for _, e := range all {
		if e.SessionID != "" {
			sessionIDByHarp[e.HarpName] = e.SessionID
		}
	}
	for _, m := range canonMetas {
		if sessionID := sessionIDByHarp[m.ID]; sessionID != "" {
			covered[sessionID] = true
		}
	}
	return covered
}

// legacyListing is the legacy leg's sessions. When it fails and canonical
// has sessions to show, the listing degrades to canonical-only (nil, nil);
// when neither leg produced anything, the error is canonical's if it failed
// too — the primary source's failure is the more actionable one — else
// legacy's.
func (f *CanonicalFallbackSource) legacyListing(ctx context.Context, canonMetas []agent.SessionMeta, canonErr error) ([]agent.SessionMeta, error) {
	legacyMetas, err := f.legacy.ListSessions(ctx)
	if err == nil {
		return legacyMetas, nil
	}
	if len(canonMetas) > 0 {
		return nil, nil
	}
	if canonErr != nil {
		return nil, canonErr
	}
	return nil, err
}
