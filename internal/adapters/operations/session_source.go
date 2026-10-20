package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// ResolvedSource is a harp's session-index entry plus the outcome of trying
// to heal (convert/refresh) its canonical transcript.
type ResolvedSource struct {
	// Entry is the harp's session-index entry, or nil when harp names no
	// entry at all (an unindexed or empty harp).
	Entry *sessions.Entry
	// Healed reports whether a conversion was attempted and succeeded.
	Healed bool
	// HealErr is set when a conversion was attempted and failed. The stored
	// transcript may still be readable; it is as fresh as the last
	// SUCCESSFUL heal left it.
	HealErr error
}

// ResolveAndHeal is the ONE source-resolution + heal seam: it resolves harp's
// session-index entry and refreshes its canonical transcript from the
// engine's own, unconditionally. A canonical file existing is not evidence
// it is COMPLETE — an earlier refresh may have caught the session part-way —
// so every caller refreshes once, whatever it believes about the session's
// state. It never chdirs.
//
// harp == "" or an unindexed harp resolves to a zero ResolvedSource with no
// error and Healed == false: there is nothing to heal.
func ResolveAndHeal(ctx context.Context, files safefs.Root, reg engine.Registry, harp string) (ResolvedSource, error) {
	if harp == "" {
		return ResolvedSource{}, nil
	}
	entry, err := GetSession(harp)
	if err != nil || entry == nil {
		return ResolvedSource{Entry: entry}, err
	}

	src := ResolvedSource{Entry: entry}
	src.Healed, src.HealErr = RefreshVendorTranscript(ctx, files, reg, *entry)

	// Re-resolve after a successful heal: a fresh conversion can populate or
	// change CanonicalTranscriptPath (computed on read — see sessions.Entry's
	// doc), and a caller reads the file the heal actually (re)wrote, not a
	// snapshot taken before it ran.
	if src.Healed {
		if refreshed, rerr := GetSession(harp); rerr == nil && refreshed != nil {
			src.Entry = refreshed
		}
	}
	return src, nil
}

// ReadRefreshFailed is the warning a read path gives when refreshing the
// session's canonical transcript failed and the read went on with the copy
// the session already has.
const ReadRefreshFailed = "refreshing its transcript from the engine's log failed; reading the copy it has"

// RefreshForRead is ResolveAndHeal for a reader that shows or resumes harp: a
// session whose run did not exit through ctxloom (killed, crashed, in a
// container, its engine run outside ctxloom) is read as its engine's log has
// it now, not as its last capture left it. It returns harp's entry as the
// refresh left it; nil, nil for an unindexed harp.
//
// A failed refresh never blocks the read: it is warned, and the reader reads
// the copy the session already has, which a failed refresh leaves as it was
// (RefreshVendorTranscript's temp-then-rename commit). A purged session is
// not refreshed: its engine's log may outlive the purge, and a read must not
// rebuild the transcript the purge destroyed.
func RefreshForRead(ctx context.Context, files safefs.Root, reg engine.Registry, harp string) (*sessions.Entry, error) {
	entry, err := GetSession(harp)
	if err != nil || entry == nil || entry.PurgedAt != nil {
		return entry, err
	}
	src, err := ResolveAndHeal(ctx, files, reg, harp)
	if err != nil {
		return nil, err
	}
	if src.HealErr != nil {
		clidiag.Warn("ctxloom", "session %s: %s: %v", harp, ReadRefreshFailed, src.HealErr)
	}
	if src.Entry == nil {
		return entry, nil
	}
	return src.Entry, nil
}

// SessionRefresh refreshes one listed session's canonical transcript and
// returns its entry as the refresh left it. ViewSessions asks it only of a
// row that has no title.
type SessionRefresh func(sessions.Entry) sessions.Entry

// ReadRefresh is the production SessionRefresh: RefreshForRead, keeping the
// listed entry when the session cannot be looked up again.
func ReadRefresh(ctx context.Context, files safefs.Root, reg engine.Registry) SessionRefresh {
	return func(e sessions.Entry) sessions.Entry {
		fresh, err := RefreshForRead(ctx, files, reg, e.HarpName)
		if err != nil {
			clidiag.Warn("ctxloom", "session %s: %s: %v", e.HarpName, ReadRefreshFailed, err)
			return e
		}
		if fresh == nil {
			return e
		}
		return *fresh
	}
}
