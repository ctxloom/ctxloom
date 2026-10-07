package operations

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/spf13/afero"
)

// `ctxloom session adopt` re-indexes vendor transcripts a rotation the index
// never recorded left ORPHANED — chiefly claude-code's /clear before the
// rotation-lineage fix: the index used to clobber the old
// binding on rebind instead of preserving it in Entry.Rotations, so every
// pre-fix /clear left a vendor file on disk with no index row, no rotation
// record, and no path FindBySessionID could ever walk back to. This file is
// the read-only discovery half (ScanAdoptCandidates); ApplyAdopt is the
// write half, and it goes through sessions.Store.AppendRotations —
// this package never hand-edits a session record (the restore that motivated this
// command was originally done exactly that way, by hand).

// AdoptVerdict is ScanAdoptCandidates' judgment on one discovered vendor
// file: whether it belongs in this harp's lineage.
type AdoptVerdict string

const (
	// AdoptVerdictAdopt means the candidate's timestamp span slots cleanly
	// into a gap in the harp's existing lineage and --yes would append it.
	AdoptVerdictAdopt AdoptVerdict = "adopt"
	// AdoptVerdictSkip means the candidate is not part of this harp's
	// lineage (already known, bound elsewhere, unreadable, or its span
	// overlaps a segment the harp already has) — see AdoptCandidate.Reason.
	AdoptVerdictSkip AdoptVerdict = "skip"
)

// The two provenances an adopted candidate's RotatedAt can have — see
// AdoptCandidate.RotatedAtSource's doc comment for what each means and why
// there is a fallback at all.
const (
	AdoptRotatedAtSuccessorFirstRecord = "successor-first-record"
	AdoptRotatedAtOwnLastRecord        = "own-last-record"
)

// AdoptCandidate is one vendor-transcript file ScanAdoptCandidates found
// sitting beside the harp's bound transcript, un-adopted, together with the
// verdict it reached about it.
type AdoptCandidate struct {
	SessionID      string
	TranscriptPath string

	// HasSpan is false for a candidate ScanAdoptCandidates never got as far
	// as reading a timestamp span for (already resolved to a harp via the
	// store lookup, so its span is irrelevant to the verdict) or could not
	// read one for at all (I/O failure, zero parseable "timestamp" fields).
	// SpanStart/SpanEnd are the zero time.Time when this is false.
	HasSpan   bool
	SpanStart time.Time
	SpanEnd   time.Time

	Verdict AdoptVerdict
	// Reason is populated for a Skip verdict and explains it (already in
	// this or another harp's lineage, unreadable, or the specific existing
	// segment it overlaps). Empty for Adopt.
	Reason string

	// RotatedAt/RotatedAtSource are populated for an Adopt verdict only:
	// the value --yes would write to the new Rotation record, and which
	// of the two rules produced it (AdoptRotatedAtSuccessorFirstRecord when
	// a later lineage member's first-record timestamp was determinable,
	// AdoptRotatedAtOwnLastRecord otherwise — the candidate's own
	// last-record timestamp, when it is the newest thing in the lineage
	// ScanAdoptCandidates could see). Shown in the dry-run table too, not
	// just written on --yes: the report reads as the exact plan --yes
	// would execute, never something a caller has to take on faith.
	RotatedAt       time.Time
	RotatedAtSource string
}

// AdoptScan is ScanAdoptCandidates' result: what it looked at and what it
// concluded about every file it found.
type AdoptScan struct {
	Harp       string
	Backend    string
	ScanDir    string
	Candidates []AdoptCandidate
}

// adoptTimelineSpan is one lineage member's timestamp span, known either
// because it is an existing binding/rotation whose vendor file
// ScanAdoptCandidates could still read, or because it is a candidate this
// scan has already decided to adopt (folded in so a SECOND candidate in the
// same run is checked against the first, not just against what the index
// already knew).
type adoptTimelineSpan struct {
	sessionID string
	start     time.Time
	end       time.Time
}

// ScanAdoptCandidates resolves harp's entry, scans the SAME directory as its
// current transcript_path for transcripts of the entry's engine not already
// reachable from the index, and judges each one against the harp's existing
// lineage. Read-only throughout: no Store write method is ever called here
// (see ApplyAdopt for the write half), so a caller can run this as many times
// as it likes without changing anything on disk.
//
// Everything engine-specific is asked of the entry's engine through the
// registry: which files are its transcripts, and of which native session
// (Engine.TranscriptSession), and when each one's records were written (a
// reader of its store offering vendorreader.RecordSpanner). An engine that
// offers neither has no store adopt can read, and is refused by that missing
// capability, naming the engine — never by its name.
func ScanAdoptCandidates(fsys afero.Fs, reg engine.Registry, harp string) (*AdoptScan, error) {
	store, err := openSessions()
	if err != nil {
		return nil, err
	}
	entry, err := store.Find(harp)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("session not found: %q", harp)
	}
	kind, ok := reg.Lookup(engine.Name(entry.Backend))
	if !ok {
		return nil, fmt.Errorf("session adopt: backend %q is not a registered engine", entry.Backend)
	}
	spanner, ok := recordSpannerFor(reg, entry.Backend)
	if !ok {
		return nil, fmt.Errorf("session adopt: backend %q supplies no transcript reader that can say when its records were written; there is no store adopt can order", entry.Backend)
	}
	if entry.TranscriptPath == "" {
		return nil, fmt.Errorf("session adopt: session %q has no transcript_path bound; nothing to scan", harp)
	}
	if _, err := kind.TranscriptSession(entry.TranscriptPath); err != nil {
		return nil, fmt.Errorf("session adopt: backend %q cannot name the session of its own transcript %s: %w", entry.Backend, entry.TranscriptPath, err)
	}
	span := func(path string) (time.Time, time.Time, int, error) { return spanner.RecordSpan(fsys, path) }
	scanDir := filepath.Dir(entry.TranscriptPath)

	timeline := existingLineageTimeline(span, *entry)

	dirEntries, err := afero.ReadDir(fsys, scanDir)
	if err != nil {
		return nil, fmt.Errorf("session adopt: scan %s: %w", scanDir, err)
	}

	var spanned, unspanned []AdoptCandidate
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		path := filepath.Join(scanDir, de.Name())
		sessionID, err := kind.TranscriptSession(path)
		if errors.Is(err, engine.ErrForeignTranscript) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("session adopt: %s: %w", path, err)
		}

		// "not the current binding, not in Rotations, not another entry's
		// binding/rotation — use the store's lookup": FindBySessionID
		// resolves ALL THREE in one call, since a session id that is
		// already this harp's current binding or already recorded in its
		// Rotations resolves right back to this same harp.
		found, ferr := store.FindBySessionID(sessionID)
		if ferr != nil {
			return nil, ferr
		}
		if found != nil {
			reason := "already in this session's lineage"
			if found.HarpName != harp {
				reason = fmt.Sprintf("bound to another session %q", found.HarpName)
			}
			unspanned = append(unspanned, AdoptCandidate{SessionID: sessionID, TranscriptPath: path, Verdict: AdoptVerdictSkip, Reason: reason})
			continue
		}

		start, end, n, serr := span(path)
		switch {
		case serr != nil:
			unspanned = append(unspanned, AdoptCandidate{SessionID: sessionID, TranscriptPath: path, Verdict: AdoptVerdictSkip, Reason: fmt.Sprintf("could not read: %v", serr)})
		case n == 0:
			unspanned = append(unspanned, AdoptCandidate{SessionID: sessionID, TranscriptPath: path, Verdict: AdoptVerdictSkip, Reason: "no parseable internal record timestamps"})
		default:
			spanned = append(spanned, AdoptCandidate{SessionID: sessionID, TranscriptPath: path, HasSpan: true, SpanStart: start, SpanEnd: end})
		}
	}

	// MEASURED ORDERING RULE: by MAX INTERNAL RECORD TIMESTAMP, never mtime
	// (external tools routinely rewrite these files' mtimes, sometimes to a
	// ms-truncated value that reads as newer than a session that actually
	// ran later — mtime ordering silently swaps a background session ahead
	// of the one that followed it). This is both the presentation order and
	// the order candidates are judged/adopted in, so the array position IS
	// the oldest-first order --yes appends in.
	sort.SliceStable(spanned, func(i, j int) bool { return spanned[i].SpanEnd.Before(spanned[j].SpanEnd) })

	// PASS 1 — verdicts. Processed in the same oldest-first order, checking
	// each candidate against a timeline that gains every EARLIER candidate
	// this pass has already adopted (so two orphans discovered together are
	// checked against each other too, not just against what the index
	// already knew) — but successor lookups do NOT happen here. A
	// candidate's true successor can be a LATER candidate in this same run
	// (id-early's successor can be id-mid, decided only after id-early), so
	// computing it during this forward pass would only ever see what has
	// been decided so far, never what comes after — see pass 2.
	for i := range spanned {
		c := &spanned[i]
		if overlap, ok := timelineOverlap(timeline, c.SpanStart, c.SpanEnd); ok {
			c.Verdict = AdoptVerdictSkip
			c.Reason = fmt.Sprintf("overlaps existing lineage segment %s (%s to %s)",
				overlap.sessionID, overlap.start.Format(time.RFC3339), overlap.end.Format(time.RFC3339))
			continue
		}
		c.Verdict = AdoptVerdictAdopt
		timeline = append(timeline, adoptTimelineSpan{sessionID: c.SessionID, start: c.SpanStart, end: c.SpanEnd})
		sort.Slice(timeline, func(i, j int) bool { return timeline[i].start.Before(timeline[j].start) })
	}

	// PASS 2 — RotatedAt. timeline now holds the FULL final lineage (every
	// existing member plus every candidate adopted this run), so each
	// adopted candidate's successor lookup sees candidates decided both
	// before AND after it in pass 1.
	for i := range spanned {
		c := &spanned[i]
		if c.Verdict != AdoptVerdictAdopt {
			continue
		}
		if successor, ok := timelineSuccessor(timeline, c.SpanEnd); ok {
			c.RotatedAt = successor.start
			c.RotatedAtSource = AdoptRotatedAtSuccessorFirstRecord
		} else {
			c.RotatedAt = c.SpanEnd
			c.RotatedAtSource = AdoptRotatedAtOwnLastRecord
		}
	}

	// Presentation order for the rows nothing could be judged on a
	// timestamp for: filename order, since there is no other meaningful
	// order to give them (their SessionID doubles as the vendor file's own
	// basename, so this is also deterministic run to run).
	sort.Slice(unspanned, func(i, j int) bool { return unspanned[i].SessionID < unspanned[j].SessionID })

	candidates := make([]AdoptCandidate, 0, len(spanned)+len(unspanned))
	candidates = append(candidates, spanned...)
	candidates = append(candidates, unspanned...)

	return &AdoptScan{Harp: harp, Backend: entry.Backend, ScanDir: scanDir, Candidates: candidates}, nil
}

// existingLineageTimeline reads a timestamp span for every lineage member
// ScanAdoptCandidates can still find bytes for: the live binding plus every
// recorded Rotation. A member whose vendor file is gone, or whose
// TranscriptPath was never recorded, is silently OMITTED rather than
// failing the scan — it contributes no gap/overlap constraint, which is the
// conservative direction: a candidate that would only have been rejected
// because of an unreadable member's span is instead judged only against
// what IS still known, never blocked on it.
func existingLineageTimeline(span func(string) (time.Time, time.Time, int, error), e sessions.Entry) []adoptTimelineSpan {
	var timeline []adoptTimelineSpan
	if e.TranscriptPath != "" {
		if start, end, n, err := span(e.TranscriptPath); err == nil && n > 0 {
			timeline = append(timeline, adoptTimelineSpan{sessionID: e.SessionID, start: start, end: end})
		}
	}
	for _, r := range e.Rotations {
		if r.TranscriptPath == "" {
			continue
		}
		if start, end, n, err := span(r.TranscriptPath); err == nil && n > 0 {
			timeline = append(timeline, adoptTimelineSpan{sessionID: r.SessionID, start: start, end: end})
		}
	}
	sort.Slice(timeline, func(i, j int) bool { return timeline[i].start.Before(timeline[j].start) })
	return timeline
}

// intervalsOverlap reports whether closed intervals [aStart,aEnd] and
// [bStart,bEnd] share any instant — including a shared boundary, since two
// segments that touch at exactly one timestamp are still the same moment
// claimed twice, not a gap between them.
func intervalsOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	return !aStart.After(bEnd) && !bStart.After(aEnd)
}

// timelineOverlap returns the first timeline member whose span overlaps
// [start,end], if any.
func timelineOverlap(timeline []adoptTimelineSpan, start, end time.Time) (adoptTimelineSpan, bool) {
	for _, s := range timeline {
		if intervalsOverlap(start, end, s.start, s.end) {
			return s, true
		}
	}
	return adoptTimelineSpan{}, false
}

// timelineSuccessor returns the timeline member with the smallest Start
// strictly after afterEnd — the item that comes next, chronologically,
// once a candidate ending at afterEnd is in place. Its Start is that
// successor's first-record timestamp, which is what a live rebind would
// have stamped as RotatedAt had the lineage machinery existed at the time
// (see BindSession's doc comment: a post-/clear successor's first record IS
// the /clear command itself, timestamped at file birth).
func timelineSuccessor(timeline []adoptTimelineSpan, afterEnd time.Time) (adoptTimelineSpan, bool) {
	var best adoptTimelineSpan
	found := false
	for _, s := range timeline {
		if !s.start.After(afterEnd) {
			continue
		}
		if !found || s.start.Before(best.start) {
			best, found = s, true
		}
	}
	return best, found
}

// ApplyAdopt appends every Adopt-verdict candidate's Rotation to harp's
// lineage through the store (sessions.Store.AppendRotations — never a hand
// edit of the record), in the oldest-first order ScanAdoptCandidates already
// computed them in. Returns how many were actually appended; 0 is a
// legitimate, non-error outcome when candidates carries no Adopt-verdict row
// (everything discovered was already known or overlapped).
func ApplyAdopt(harp string, candidates []AdoptCandidate) (int, error) {
	store, err := openSessions()
	if err != nil {
		return 0, err
	}
	var rotations []sessions.Rotation
	for _, c := range candidates {
		if c.Verdict != AdoptVerdictAdopt {
			continue
		}
		rotations = append(rotations, sessions.Rotation{
			SessionID:      c.SessionID,
			TranscriptPath: c.TranscriptPath,
			RotatedAt:      c.RotatedAt,
		})
	}
	if len(rotations) == 0 {
		return 0, nil
	}
	if err := store.AppendRotations(harp, rotations); err != nil {
		return 0, err
	}
	return len(rotations), nil
}
