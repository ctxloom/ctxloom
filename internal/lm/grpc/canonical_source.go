package grpc

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// This file is the S4 consumer cutover: CanonicalFallbackSource is
// the transitional pb.SessionSource every production reader (compactor, MCP
// memory tools, `memory`/`session` CLI commands) is flipped onto. It prefers
// ctxloom's own captured transcript.jsonl (transcript.CanonicalHistory,
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
//
// S5: the four broken per-engine scrapers (codex, kiro,
// antigravity, claude-code) were DELETED outright (the user's explicit
// decision — not demoted to a fixture-pinned importer, §4c/§4d of the
// removal plan). retiredScraperBackends names them. A caller building a
// source for one of those backends passes legacy=nil: canonical capture is
// the ONLY source, matching the delete decision (no legacy leg to ever fall
// back to, since there is no reader left to fall back onto). opencode is
// deliberately absent from that set — its native reader
// (internal/opencode/capabilities.go) is correct and stays wired as the
// fallback leg.

// retiredScraperBackends names the backends whose legacy per-engine
// SessionHistory scraper was removed in S5 (proven broken:
// codex's envelope-vs-flat parse, tall-grab's claude wrong-filename /
// kiro v1-vs-v2-sqlite / antigravity global-store mis-key).
// A backend named here has no History() implementation left — its
// Backend.History() now returns nil — so a caller resolving a SessionSource
// for it must not construct a legacy leg at all (there is nothing there to
// ask). Every other backend (opencode's native reader, acp's inert
// not-yet-supported stub, any future backend) keeps its legacy leg.
//
// Unexported: a roster is data this package owns, and an exported map is
// writable by every importer. Reach it through IsRetiredScraperBackend or
// RetiredScraperBackendNames — the same shape the sibling engine rosters use
// (operations.VendorImportEngineNames, isolation.ComposableEngines).
var retiredScraperBackends = map[string]bool{
	"codex":       true,
	"kiro":        true,
	"antigravity": true,
	"claude-code": true,
}

// IsRetiredScraperBackend reports whether backendName lost its legacy scraper
// in S5, i.e. whether a SessionSource for it must be built with legacy=nil.
func IsRetiredScraperBackend(backendName string) bool {
	return retiredScraperBackends[backendName]
}

// RetiredScraperBackendNames returns the roster, sorted, as a fresh slice the
// caller may keep or reorder without reaching this package's own copy.
func RetiredScraperBackendNames() []string {
	names := make([]string, 0, len(retiredScraperBackends))
	for name := range retiredScraperBackends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CanonicalFallbackSource wraps a legacy SessionSource with canonical-first
// selection. Store resolves a backend-native session id to the harp that owns
// it (the reverse of the index's forward SessionID lookup) so GetSession's
// sessionID parameter — always backend-native at every call site in this
// codebase — can still be checked against the canonical store, which is
// harp-keyed.
//
// legacy may be nil (S5): for a retired-scraper backend there is no
// legacy reader to fall back to at all, so every method serves canonical-only
// and degrades to canonical's own "not found"/"empty" contract instead of
// ever dereferencing legacy.
type CanonicalFallbackSource struct {
	legacy    SessionSource // nil for a retired-scraper backend (S5): canonical-only
	canonical *transcript.CanonicalHistory
	store     sessions.Store
}

var _ SessionSource = (*CanonicalFallbackSource)(nil)

// NewCanonicalFallbackSource returns a CanonicalFallbackSource scoped to
// workDir (the project the canonical enumeration/CurrentSession is limited
// to, matching legacy's own self-situated project scoping). Pass legacy=nil
// for a retired-scraper backend (S5) — canonical becomes the sole
// source, never falling back.
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

// GetSession resolves id — which callers pass as EITHER a harp OR a
// backend-native session id, see below — and prefers the canonical
// transcript when one is captured; otherwise falls back to the legacy source
// keyed directly by id, unchanged from pre-S4 behavior. When legacy is nil
// (S5, a retired-scraper backend) there is nothing to fall back to: no
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
	var uncaptured *transcript.NoCanonicalTranscriptError
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
		// the harp and the concrete remedy (`ctxloom session backfill <harp>`),
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
// capture) and a legacy source exists. When legacy is nil (S5) canonical's
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
// list. When legacy is nil (S5) the listing is canonical-only — this also
// removes the codex relative-filename-ID dedup quirk S4 flagged (codex's
// deleted ListSessions returned a relPath as ID, which never matched the
// index's SessionID and so never deduped against canonical; there is simply
// no legacy leg left to produce that mismatch).
func (f *CanonicalFallbackSource) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	canonMetas, canonErr := f.canonical.ListSessions(ctx)

	if f.legacy == nil {
		// No legacy leg to fall back to (retired scrapers: codex, kiro,
		// antigravity, claude-code): a failed canonical read is the WHOLE
		// listing's failure, not "zero sessions". Discarding canonErr
		// here used to report a confident empty list indistinguishable from a
		// project that genuinely has none.
		if canonErr != nil {
			return nil, canonErr
		}
		sort.SliceStable(canonMetas, func(i, j int) bool {
			return canonMetas[i].StartTime.After(canonMetas[j].StartTime)
		})
		return canonMetas, nil
	}

	// One index read for the whole dedup set, not one per canonical session:
	// every Find re-reads and re-parses the entire index file, so the per-session
	// lookup made the cost of a listing grow with the size of the project for
	// data a single read already carries. An unreadable index simply covers
	// nothing, exactly as a failing Find did.
	covered := make(map[string]bool, len(canonMetas))
	if f.store != nil && len(canonMetas) > 0 {
		if idx, err := f.store.Load(); err == nil {
			sessionIDByHarp := make(map[string]string, len(idx.Sessions))
			for _, e := range idx.Sessions {
				if e.SessionID != "" {
					sessionIDByHarp[e.HarpName] = e.SessionID
				}
			}
			for _, m := range canonMetas {
				if sessionID := sessionIDByHarp[m.ID]; sessionID != "" {
					covered[sessionID] = true
				}
			}
		}
	}

	legacyMetas, err := f.legacy.ListSessions(ctx)
	if err != nil {
		if len(canonMetas) == 0 {
			// Neither leg produced anything: prefer canonical's error when BOTH
			// failed — it is the primary source now (this file's own doc), so
			// its failure is the more actionable one to surface.
			if canonErr != nil {
				return nil, canonErr
			}
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
