package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
