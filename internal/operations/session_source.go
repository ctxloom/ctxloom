package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// Liveness tells ResolveAndHeal how to treat a harp's canonical transcript
// when deciding whether to heal (convert/refresh) it.
//
// Every value below causes ResolveAndHeal to refresh — the pre-unification
// design considered a presence-guarded "skip if a canonical file already
// exists" verb for a finished session (ConvertVendorTranscript's shape), but
// that is exactly what made pty-exit capture (cli.convertVendorTranscriptOnExit)
// a permanent no-op once ANY canonical transcript existed: a mid-session
// /recover materializes one, and everything the session did afterward was
// silently lost at exit. A canonical file existing is
// not evidence it is COMPLETE: LivenessFinished means refresh once, never skip on presence.
//
// The three values stay distinct — rather than collapsing to one — because
// they document DIFFERENT REASONS a caller is asking (a session still being
// written to vs. one this call believes is over vs. a CLI process that
// cannot tell either way), which is call-site-relevant even though today's
// ResolveAndHeal treats them identically; a future caller that genuinely
// wants the old skip-on-presence behavior can be given a fourth value
// without renaming the existing three out from under their callers.
type Liveness int

const (
	// LivenessFinished: this call is the last look this caller expects to
	// take at harp's transcript (e.g. process exit) — refresh once,
	// unconditionally.
	LivenessFinished Liveness = iota
	// LivenessLive: the session is still being written to right now (e.g.
	// recover_session, mid-conversation) — refresh every call, since the
	// source can have grown since the last one.
	LivenessLive
	// LivenessUnknown: the caller cannot tell whether the session can still
	// grow (a CLI process invoked against an arbitrary harp) — treated like
	// LivenessFinished: refresh once, never assume the cache is fresh.
	LivenessUnknown
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
	// StampedSize is the byte size recorded when the harp's essence was last
	// distilled (Entry.SourceSize) — the fingerprint EssenceCurrent compares
	// SourcePath's live size against. Zero when never distilled.
	StampedSize int64
}

// ResolveAndHeal is the ONE source-resolution + heal seam every distillation
// path funnels through: it resolves harp's session-index entry and brings
// its canonical transcript up to date per live (see the Liveness doc). It
// never chdirs — callers that need a particular working directory situate
// the process themselves (see CompactEntry's doc for why that split exists).
//
// harp == "" or an unindexed harp resolves to a zero ResolvedSource with no
// error and Healed == false: there is nothing to heal.
func ResolveAndHeal(ctx context.Context, harp string, live Liveness) (ResolvedSource, error) {
	if harp == "" {
		return ResolvedSource{}, nil
	}
	entry, err := GetSession(harp)
	if err != nil || entry == nil {
		return ResolvedSource{Entry: entry}, err
	}

	src := ResolvedSource{Entry: entry}
	switch live {
	case LivenessLive, LivenessFinished, LivenessUnknown:
		// All three refresh today — see the Liveness doc for why they stay
		// distinct enum values anyway.
		src.Healed, src.HealErr = RefreshVendorTranscript(ctx, *entry)
	}

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
	src.StampedSize = entry.SourceSize
	return src, nil
}

// EssenceCurrent is the ONE staleness predicate every distillation path's
// cache check funnels through, folding together what were three separate
// implementations (sessions.Entry.SourceStale, the MCP recover path's direct
// sessions.TranscriptStale + MaxEssenceChars bound, and cli's resume picker's
// "essence file simply absent" check):
//
//   - an empty or over-MaxEssenceChars cached body is never current, and that
//     is always KNOWN (an oversized essence from an older binary — the size
//     cap was added after some essences were already written — must not be
//     trusted just because staleness happens to look fine);
//   - a heal that was attempted and failed (src.HealErr != nil) forfeits the
//     cache hit: the size being compared against is whatever the last
//     successful heal left behind, not a true fact about this call;
//   - otherwise, current is the inverse of sessions.TranscriptStale(src.
//     SourcePath, src.StampedSize), and known carries forward unchanged —
//     callers that want to trust an indeterminate result anyway (an archived
//     session rarely changes) apply that bias themselves; this predicate only
//     answers what it can prove.
func EssenceCurrent(src ResolvedSource, cached []byte) (current, known bool) {
	if len(cached) == 0 || len(cached) > memory.MaxEssenceChars || src.HealErr != nil {
		return false, true
	}
	stale, known := sessions.TranscriptStale(src.SourcePath, src.StampedSize)
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
