package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// ResolvedSource is a harp's session-index entry plus the outcome of trying
// to heal (convert/refresh) its canonical transcript — the shared result
// every distillation path resolves down to before it either reads from the
// source or reuses a cached essence.
type ResolvedSource struct {
	// Entry is the harp's session-index entry, or nil when harp names no
	// entry at all (an unindexed or empty harp).
	Entry *sessions.Entry
	// Healed reports whether a conversion was attempted and succeeded.
	Healed bool
	// HealErr is set when a conversion was attempted and failed. The stored
	// transcript may still be readable, but a caller must not treat the
	// cached essence's staleness fingerprint as trustworthy: the size it
	// would be compared against is whatever the last SUCCESSFUL heal left
	// behind, not this call's.
	HealErr error

	// SourcePath is the transcript path staleness is measured against
	// (CanonicalTranscriptPath when captured, else the legacy TranscriptPath),
	// resolved AFTER the heal attempt above so a fresh conversion's path is
	// what staleness compares against.
	SourcePath string
	// StampedEntries is the entry count recorded when the harp's essence was last
	// distilled (Entry.SourceEntries) — the fingerprint EssenceCurrent compares
	// SourcePath's live size against. Zero when never distilled.
	StampedEntries int
}

// ResolveAndHeal is the ONE source-resolution + heal seam every distillation
// path funnels through: it resolves harp's session-index entry and refreshes
// its canonical transcript, unconditionally. A canonical file existing is
// not evidence it is COMPLETE — a mid-session /recover materializes one, and
// a presence-guarded skip would silently lose everything the session did
// afterward — so every caller refreshes once, whatever it believes about the
// session's state. It never chdirs — callers that need a particular working
// directory situate the process themselves (see CompactEntry's doc for why
// that split exists).
//
// harp == "" or an unindexed harp resolves to a zero ResolvedSource with no
// error and Healed == false: there is nothing to heal.
func ResolveAndHeal(ctx context.Context, harp string) (ResolvedSource, error) {
	if harp == "" {
		return ResolvedSource{}, nil
	}
	entry, err := GetSession(harp)
	if err != nil || entry == nil {
		return ResolvedSource{Entry: entry}, err
	}

	src := ResolvedSource{Entry: entry}
	src.Healed, src.HealErr = RefreshVendorTranscript(ctx, *entry)

	// Re-resolve after a successful heal: a fresh conversion can populate or
	// change CanonicalTranscriptPath (computed on read — see sessions.Entry's
	// doc), and staleness must compare against the file the heal actually
	// (re)wrote, not a snapshot taken before it ran.
	if src.Healed {
		if refreshed, rerr := GetSession(harp); rerr == nil && refreshed != nil {
			entry = refreshed
			src.Entry = refreshed
		}
	}

	src.SourcePath = entry.TranscriptPath
	if entry.CanonicalTranscriptPath != "" {
		src.SourcePath = entry.CanonicalTranscriptPath
	}
	src.StampedEntries = entry.SourceEntries
	return src, nil
}

// EssenceCurrent is the ONE staleness predicate every distillation path's
// cache check funnels through:
//
//   - an empty or over-MaxEssenceChars cached body is never current, and that
//     is always KNOWN (an oversized essence from an older binary — the size
//     cap was added after some essences were already written — must not be
//     trusted just because staleness happens to look fine);
//   - a heal that was attempted and failed (src.HealErr != nil) forfeits the
//     cache hit: the size being compared against is whatever the last
//     successful heal left behind, not a true fact about this call;
//   - otherwise, current is the inverse of sessions.TranscriptStale(src.
//     SourcePath, src.StampedEntries), and known carries forward unchanged —
//     callers that want to trust an indeterminate result anyway (an archived
//     session rarely changes) apply that bias themselves; this predicate only
//     answers what it can prove.
func EssenceCurrent(src ResolvedSource, cached []byte) (current, known bool) {
	if len(cached) == 0 || len(cached) > memory.MaxEssenceChars || src.HealErr != nil {
		return false, true
	}
	stale, known := sessions.TranscriptStale(src.SourcePath, src.StampedEntries)
	return !stale, known
}

// DistillEntry runs the compactor for src.Entry and returns the result — the
// ONE distill call a caller reaches once ResolveAndHeal has resolved a
// source and EssenceCurrent has decided the cache can't be trusted. It is
// CompactEntry addressed by ResolvedSource instead of a bare *sessions.Entry,
// so a caller that already paid for source resolution does not re-resolve.
//
// Budget bounding and singleflight dedup are NOT here: they matter only to
// the long-lived MCP host relay fielding concurrent tool calls for the same
// session, and stay there (withDistillBudget, singleflightDistill) rather
// than becoming a concern every one-shot CLI caller has to reason about too.
func DistillEntry(ctx context.Context, src ResolvedSource, cfg *config.Config, opts DistillOptions) (*memory.CompactionResult, error) {
	if src.Entry == nil {
		return nil, fmt.Errorf("nothing to distill: session not found in the index")
	}
	return CompactEntry(ctx, src.Entry, cfg, opts)
}
