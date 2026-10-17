package sessions

import (
	"errors"
	"fmt"
	"time"
)

// A session's transcript is its record: once engines own compaction there is
// no summary standing in for it, so every destroyer of transcripts — the
// reaper's persist scope (`clean --include-persist`), `session transcript
// purge`, `session purge`, `session remove` and the sweep — asks Retention
// first.
var (
	// ErrTranscriptKept is the refusal for a session marked keep
	// (Entry.Keep, `ctxloom session keep`).
	ErrTranscriptKept = errors.New("the session is marked keep")
	// ErrTranscriptRecent is the refusal for a session that ended after the
	// retention cutoff — or that nothing proves ended at all.
	ErrTranscriptRecent = errors.New("the session ended within the transcript retention period")
	// ErrNoRetentionBound is the refusal when a caller asks to destroy a
	// transcript without stating the retention cutoff. An error, not a
	// default, at this layer: the default lives with the config key above
	// it, and the function that gates a deletion must not be callable in a
	// shape that gates nothing.
	ErrNoRetentionBound = errors.New("destroying a session transcript requires an explicit retention cutoff")
)

// Retention is the rule that decides when a session's transcript may be
// destroyed: once the session ENDED at or before Cutoff (now minus the
// session_transcript_retention age), unless it is marked keep.
type Retention struct {
	// Cutoff is the retention bound. Required: zero refuses
	// (ErrNoRetentionBound).
	Cutoff time.Time
}

// Ended is when e ended, for retention: the LATER of its recorded end and
// lastActive (ActivityTime). A session resumed after its recorded end, or one
// that died before recording an end, did not end before it was last worked
// on. Zero when neither is known.
func Ended(e *Entry, lastActive time.Time) time.Time {
	end := lastActive
	if e != nil && e.EndedAt != nil && e.EndedAt.After(end) {
		end = *e.EndedAt
	}
	return end
}

// Retains reports why e's transcript must be kept — ErrTranscriptKept,
// ErrTranscriptRecent, ErrNoRetentionBound, each matched with errors.Is — or
// nil when it is reclaimable. lastActive is the session's ActivityTime.
//
// An internal one-shot (OriginOneShot) is a machine's single turn that
// nobody resumes, never a human's record: reclaimable at any age, unless
// marked keep.
func (r Retention) Retains(e *Entry, lastActive time.Time) error {
	if r.Cutoff.IsZero() {
		return ErrNoRetentionBound
	}
	if e != nil && e.Keep {
		return ErrTranscriptKept
	}
	if e != nil && e.Origin == OriginOneShot {
		return nil
	}
	end := Ended(e, lastActive)
	if end.IsZero() {
		return fmt.Errorf("%w: nothing records when it ended", ErrTranscriptRecent)
	}
	if end.After(r.Cutoff) {
		return fmt.Errorf("%w: it ended %s, after the cutoff %s", ErrTranscriptRecent,
			end.UTC().Format(time.RFC3339), r.Cutoff.UTC().Format(time.RFC3339))
	}
	return nil
}
