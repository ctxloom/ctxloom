package transcript

import (
	"context"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// CanonicalFallbackSource is the Source every production reader (compactor,
// MCP memory tools, `memory`/`session` CLI commands) reads through: ctxloom's
// own captured transcript.jsonl (CanonicalHistory) is the only transcript
// source, since no shipped engine keeps a readable store of its own.
//
// What it adds over CanonicalHistory is id resolution. Store resolves a
// backend-native session id to the harp that owns it (the reverse of the
// index's forward SessionID lookup), so GetSession accepts either a harp or a
// backend-native session id and still reads the harp-keyed canonical store.
type CanonicalFallbackSource struct {
	canonical *CanonicalHistory
	store     sessions.Store
}

var _ Source = (*CanonicalFallbackSource)(nil)

// NewCanonicalFallbackSource returns a CanonicalFallbackSource scoped to
// workDir, the project the canonical enumeration and CurrentSession are
// limited to.
func NewCanonicalFallbackSource(workDir string, store sessions.Store) *CanonicalFallbackSource {
	return &CanonicalFallbackSource{
		canonical: NewCanonicalHistory(workDir, store),
		store:     store,
	}
}

// harpForSessionID reverse-resolves a backend-native session id to its owning
// harp via the index, or "" when unbound/unknown. A best-effort, read-only
// lookup: an index error degrades to "", and GetSession then reports the
// first canonical attempt's error.
//
// Store.FindBySessionID is the ONE definition of "which harp owns this id",
// and it is a LINEAGE lookup: it matches an id the harp is currently bound to
// AND any id a /clear rebind has displaced into Entry.Rotations. Both count.
// The id recover_session targets after a context wipe is precisely a
// rotated-away one — the pre-clear thread — and a scan of the current binding
// alone could not map it to its harp, and the caller would be told there was
// nothing to recover.
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
// backend-native session id, see below — to its canonical transcript. No
// canonical transcript for a resolvable harp, or an unresolvable id, is a
// genuine "no session".
//
// id is resolved HARP-FIRST, not just as a backend-native
// session id. `memory list`'s SESSION ID column literally displays the harp
// for any canonical-backed session (CanonicalHistory.ListSessions sets
// meta.ID = harp — the sessionID field never surfaces to a user at all), so
// `memory show <that value>` must resolve it — the direct harp lookup
// `session compact` already does successfully via the index, unlike the reverse
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

	// The id did not name a harp with a readable transcript: try it as a
	// backend-native session id.
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
		// No canonical transcript (or it failed to parse) for the harp the id
		// resolved to: that canonical-side error IS the answer.
		return nil, err
	}
	// The id resolves to no harp, so the first attempt's error is the answer:
	// a NoCanonicalTranscriptError names the harp and the concrete remedy
	// (importing the vendor transcript), and any other error — corrupt,
	// truncated, a schema this build refuses to guess at — is a real one that
	// must not be reported as absence.
	return nil, firstErr
}

// CurrentSession is the project's most-recently-active canonical-backed
// session, with canonical's own "no sessions" contract (nil, nil).
func (f *CanonicalFallbackSource) CurrentSession(ctx context.Context) (*agent.Session, error) {
	return f.canonical.CurrentSession(ctx)
}

// ListSessions is this project's canonical-backed sessions, newest first. A
// failed canonical read is the WHOLE listing's failure, not "zero sessions":
// discarding it would report a confident empty list indistinguishable from a
// project that genuinely has none.
func (f *CanonicalFallbackSource) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	metas, err := f.canonical.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	sortNewestFirst(metas)
	return metas, nil
}

// sortNewestFirst orders sessions by start time, newest first, stably.
func sortNewestFirst(metas []agent.SessionMeta) {
	sort.SliceStable(metas, func(i, j int) bool {
		return metas[i].StartTime.After(metas[j].StartTime)
	})
}
