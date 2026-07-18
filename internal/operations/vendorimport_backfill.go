package operations

import (
	"context"
	"sort"

	"github.com/ctxloom/ctxloom/internal/sessions"
)

// BackfillResult is the outcome of running ConvertVendorTranscript over a set
// of already-indexed sessions — `session backfill`'s result, one entry's
// ConvertVendorTranscript run over MANY. Harp names, not full Entry values:
// this is a report a human or the VSCode companion renders, not a session
// listing (`session list` already owns that projection).
type BackfillResult struct {
	// Converted lists harps an import actually ran for, in the order they
	// were processed.
	Converted []string `json:"converted,omitempty"`
	// Skipped counts entries ConvertVendorTranscript no-op'd on (unregistered
	// backend, no locatable vendor transcript, or already-canonical) — the
	// overwhelming majority on a typical index (most sessions already ran
	// structured/ACP and have a canonical transcript from the live tee).
	Skipped int `json:"skipped"`
	// Failed maps harp -> error string for entries where a vendor transcript
	// WAS located but Convert or Recorder construction failed. Kept separate
	// from Skipped so a caller (and a human reading the text render) can see
	// genuine problems distinctly from the ordinary "nothing to do" case.
	Failed map[string]string `json:"failed,omitempty"`
}

// BackfillVendorTranscripts runs ConvertVendorTranscript over every entry in
// entries, in order, and never stops early on one entry's failure — a single
// unconvertable session (a corrupt vendor file, a transient stat failure)
// must not block every other session's backfill (CLAUDE.md fault tolerance;
// mirrors session_cmd.go's distillMissingOrStale, which applies the same
// per-entry warn-and-continue rule to distillation).
func BackfillVendorTranscripts(ctx context.Context, entries []sessions.Entry) BackfillResult {
	var res BackfillResult
	for _, e := range entries {
		converted, err := ConvertVendorTranscript(ctx, e)
		switch {
		case err != nil:
			if res.Failed == nil {
				res.Failed = make(map[string]string)
			}
			res.Failed[e.HarpName] = err.Error()
		case converted:
			res.Converted = append(res.Converted, e.HarpName)
		default:
			res.Skipped++
		}
	}
	return res
}

// FailedHarps returns r.Failed's keys, sorted — the deterministic iteration
// order a text/table render needs over an otherwise order-nondeterministic
// map.
func (r BackfillResult) FailedHarps() []string {
	harps := make([]string, 0, len(r.Failed))
	for h := range r.Failed {
		harps = append(harps, h)
	}
	sort.Strings(harps)
	return harps
}
