package liveness

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// TranscriptStat is the CHEAPEST evidence of life, and the only evidence that
// would have caught the 2026-07-24 incident on its own: a projection of one
// harp's canonical transcript.jsonl onto the shared progress definition (see
// this package's doc comment).
//
// Every field is a MEASUREMENT, never a verdict. The ladder in monitor.go is
// the only thing allowed to draw a conclusion, so that the definition and its
// application stay separable and separately arguable.
type TranscriptStat struct {
	// Observed reports that this projection was reached by actually LOOKING
	// at the path — the same absent-vs-error discipline ProcState.Observed
	// carries, and for the same reason. It is true for a file read to a
	// conclusion AND for a file conclusively absent (see Exists); it is
	// FALSE when the observation itself broke: the path could not be opened,
	// the scan aborted part-way, or the caller never asked. Every rule that
	// draws a conclusion from ABSENCE must check this first — without it,
	// "the engine emitted zero events" and "we could not look" are the same
	// zero value, which is how a broken observation becomes a confident
	// stall verdict.
	Observed bool `json:"observed"`
	// Truncated reports that the file was longer than the scan bound
	// (maxTranscriptScan) so Records, Malformed, EntryRecords,
	// AssistantEntries, EntryTypes, Completes and Redelivery describe only
	// the HEAD of the file. The last-record measurements (LastTS, MaxSeq,
	// TurnClosed, PendingPermission, LastToolUse) are recovered from a tail
	// pass and stay trustworthy; the counting fields are floors, never
	// totals, and no absence-based rule may fire on them.
	Truncated bool `json:"truncated,omitempty"`
	// Exists reports whether the file was there at all. FALSE IS A SIGNAL,
	// NOT AN ERROR: transcript.NewRecorder opens lazily on first successful
	// Record, so no file means the engine emitted literally zero events.
	// Only meaningful when Observed is true.
	Exists bool `json:"exists"`
	// Records is the number of parseable JSONL lines.
	Records int `json:"records"`
	// Malformed is the number of lines that failed to parse. Non-zero is
	// worth surfacing (a truncated tail is normal for a file being appended
	// to concurrently; a large count is corruption) but is never on its own a
	// stall verdict.
	Malformed int `json:"malformed,omitempty"`
	// MaxSeq is the highest envelope seq seen. Seq restarts at 0 for each new
	// transcript.Recorder against the same O_APPEND file, so
	// `Records > 1 && MaxSeq == 0` means every line came from a DIFFERENT
	// recorder instance — the relaunch loop. See SeqPinned.
	MaxSeq int `json:"max_seq"`
	// SeqPinned is that signature, precomputed: more than one record, none of
	// which ever advanced past seq 0.
	SeqPinned bool `json:"seq_pinned,omitempty"`
	// EntryRecords counts records with kind=="entry".
	EntryRecords int `json:"entry_records"`
	// EntryTypes is the DISTINCT set of .entry.type values, sorted. The
	// failing agents had exactly one member: "user".
	EntryTypes []string `json:"entry_types,omitempty"`
	// AssistantEntries counts entries of type "assistant" — the definition's
	// "at least one" clause.
	AssistantEntries int `json:"assistant_entries"`
	// Completes counts kind=="complete" turn-boundary records.
	Completes int `json:"completes"`
	// TurnClosed reports whether a `complete` record appears AFTER the last
	// `user` entry — i.e. the in-flight turn actually finished. A dead process
	// with TurnClosed false is the DIED case; with TurnClosed true it merely
	// ended.
	TurnClosed bool `json:"turn_closed"`
	// PendingPermission reports that the LAST record is a permission request
	// with nothing after it: the agent is parked waiting on a decision. This
	// is the transcript-side half of "never reap a child awaiting approval",
	// independent of whatever the roster claims.
	PendingPermission bool `json:"pending_permission,omitempty"`
	// FirstTS / LastTS are the receipt times of the first and last records.
	FirstTS time.Time `json:"first_ts,omitempty"`
	LastTS  time.Time `json:"last_ts,omitempty"`
	// LastToolUse is the receipt time of the newest entry of type "tool_use"
	// — "time since last tool_use" in the signal list.
	LastToolUse time.Time `json:"last_tool_use,omitempty"`
	// Redelivery is non-nil when identical content was re-appended on a fixed
	// cadence. This is POSITIVE evidence of a loop and therefore needs no
	// grace period at all, unlike every absence-based rule.
	Redelivery *Redelivery `json:"redelivery,omitempty"`
}

// Redelivery describes one repeated-content group that arrived on a regular
// cadence — the incident's fingerprint (the same composed-context block
// re-appended every ~4-5 seconds).
type Redelivery struct {
	// EntryType is the .entry.type of the repeated records.
	EntryType string `json:"entry_type"`
	// Repeats is how many times the identical content was recorded.
	Repeats int `json:"repeats"`
	// Cadence is the MEDIAN gap between consecutive repeats.
	Cadence time.Duration `json:"cadence"`
	// MaxDeviation is the largest departure of any single gap from Cadence.
	// A loop is regular; a human re-sending the same text is not.
	MaxDeviation time.Duration `json:"max_deviation"`
	// Sample is a short prefix of the repeated content, for the reason text.
	Sample string `json:"sample,omitempty"`
}

// envelope is the subset of transcript.Record this package reads. Deliberately
// a LOCAL, minimal struct rather than an import of transcript.Record: the
// monitor must survive a transcript written by a newer schema version without
// failing to parse (unknown fields are ignored by encoding/json), and it must
// not acquire a dependency edge that would make internal/transcript unable to
// ever depend on liveness.
type envelope struct {
	Seq   int       `json:"seq"`
	TS    time.Time `json:"ts"`
	Kind  string    `json:"kind"`
	Entry *struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	} `json:"entry"`
}

// maxTranscriptScan bounds how many lines ReadTranscript will parse from the
// HEAD of the file. A runaway re-delivery loop is exactly the case where the
// file grows without bound, so the monitor reading it must not itself become
// the expensive thing — and the COUNTING signals it needs (seq pinning, entry
// variety, a repeat cadence) are all established within a handful of records.
// 20000 lines is far past any threshold in this package while staying a
// sub-second read.
//
// The LAST-RECORD signals are the opposite: LastTS, TurnClosed, MaxSeq and
// PendingPermission live at the END of the file and drive four rungs each. A
// head-only bound made them permanently stale past 20000 lines, so a healthy
// long-running agent read as stalled and a cleanly-finished one as dead
// (U056-F02). maxTailBytes is the bounded suffix re-read to recover them.
const maxTranscriptScan = 20000

// maxTailBytes bounds the suffix re-read that recovers the last-record
// measurements from a transcript longer than maxTranscriptScan. It is a BYTE
// bound rather than a line bound because it is applied by seeking, which is
// what keeps the tail pass O(1) in the file's length — the whole point of
// having a bound at all. 1 MiB comfortably holds the trailing records of any
// engine while staying one read.
const maxTailBytes = 1 << 20

// maxLineBytes bounds one JSONL line. A composed-context block re-delivered in
// a loop is large, so the default bufio.Scanner limit (64 KiB) is genuinely
// too small here — and silently dropping the very lines that prove the loop is
// the failure this package is about.
const maxLineBytes = 4 << 20

// ReadTranscript projects the canonical transcript at path onto the shared
// progress definition.
//
// A MISSING FILE IS NOT AN ERROR. It returns Exists:false with a nil error,
// because absence is itself the strongest possible statement about progress:
// transcript.NewRecorder creates the file lazily on the first successful
// Record, so no file means zero events were ever produced. Callers that
// treated os.IsNotExist as a failure would turn the loudest signal into a
// swallowed error — the exact laundering this package refuses to do.
//
// A file that exists but cannot be OPENED (permissions, a directory in its
// place) IS an error: that is a broken observation, not an observation of
// nothing.
func ReadTranscript(path string) (TranscriptStat, error) {
	return ReadTranscriptWith(path, DefaultThresholds())
}

// ReadTranscriptWith is ReadTranscript under explicit tuning. Only the
// re-delivery detector reads thr; every other measurement is threshold-free by
// construction, so that a disagreement about tuning can never turn into a
// disagreement about what the file SAYS.
func ReadTranscriptWith(path string, thr Thresholds) (TranscriptStat, error) {
	thr = thr.normalize()
	if path == "" {
		return TranscriptStat{}, errors.New("liveness: ReadTranscript needs a path")
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Conclusively absent — that IS an observation, and the loudest
			// one this package has.
			return TranscriptStat{Observed: true, Exists: false}, nil
		}
		// A broken instrument. Observed stays false so no absence rule can
		// mistake this for "the engine emitted zero events" (U056-F01/F05).
		return TranscriptStat{}, fmt.Errorf("liveness: open transcript %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sn := newTxScan()
	truncated, err := scanHead(f, sn)
	if err != nil {
		return unobserved(sn.finish(thr)), fmt.Errorf("liveness: read transcript %s: %w", path, err)
	}
	if truncated {
		if err := scanTail(f, sn); err != nil {
			return unobserved(sn.finish(thr)), fmt.Errorf("liveness: read transcript tail %s: %w", path, err)
		}
	}
	st := sn.finish(thr)
	st.Truncated = truncated
	return st, nil
}

// unobserved stamps a partial projection as what it is. A scan that aborted
// part-way leaves REAL counts from the lines it did read, and those counts are
// exactly what the absence rules would condemn on ("zero assistant turns in 0
// records"), so the partial data is kept for the evidence trail and the bit
// that makes it unusable as proof of absence is cleared.
func unobserved(st TranscriptStat) TranscriptStat {
	st.Observed = false
	return st
}

// scanHead folds the bounded head of f. truncated reports that the bound was
// hit with lines still unread — i.e. that every counting field is now a floor
// and the last-record fields need scanTail.
func scanHead(f *os.File, sn *txScan) (truncated bool, err error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		if sn.st.Records+sn.st.Malformed >= maxTranscriptScan {
			return true, nil
		}
		sn.line(sc.Bytes())
	}
	return false, sc.Err()
}

// scanTail re-reads a bounded SUFFIX of f to recover the last-record
// measurements the head bound cannot see. It seeks rather than continuing the
// head scan, so its cost is independent of how far the file has run — which is
// the property the bound exists to protect.
//
// The first line after a non-zero seek is discarded: a byte offset lands
// mid-record, and half a JSONL line is not a malformed record, it is not a
// record at all.
func scanTail(f *os.File, sn *txScan) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	off := info.Size() - maxTailBytes
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	if off > 0 && !sc.Scan() {
		return sc.Err()
	}
	sn.tail = &txTail{userIdx: -1, completeIdx: -1}
	for sc.Scan() {
		sn.tailLine(sc.Bytes())
	}
	return sc.Err()
}

// txScan folds a transcript's lines into a TranscriptStat. Split out of
// ReadTranscriptWith so each step of the fold is separately readable (and so
// the reader stays under the project's cyclomatic-complexity gate).
type txScan struct {
	st    TranscriptStat
	types map[string]bool
	// groups keys (entry type, content) to the receipt times it arrived at —
	// the re-delivery cadence measurement.
	groups          map[[2]string][]time.Time
	lastUserIdx     int
	lastCompleteIdx int
	lastKind        string
	// tail is the bounded suffix fold, non-nil only when the head bound was
	// hit. Its measurements OVERRIDE the head's for every last-record field,
	// because on a truncated file the head's answer is not merely imprecise,
	// it is about a different part of the file.
	tail *txTail
}

// txTail is the last-record fold: the handful of measurements that describe
// where the transcript ENDED rather than how much of it there is. Kept
// separate from txScan's counting state so the tail pass can never inflate a
// count it only saw part of.
type txTail struct {
	lastTS      time.Time
	maxSeq      int
	lastToolUse time.Time
	lastKind    string
	// userIdx/completeIdx are positions WITHIN the tail (-1 = not seen), which
	// is all TurnClosed needs: whether a `complete` came after the last `user`.
	userIdx     int
	completeIdx int
	n           int
}

func newTxScan() *txScan {
	return &txScan{
		st:              TranscriptStat{Exists: true, Observed: true},
		types:           map[string]bool{},
		groups:          map[[2]string][]time.Time{},
		lastUserIdx:     -1,
		lastCompleteIdx: -1,
	}
}

// line folds one raw JSONL line. An unparseable line is COUNTED, never fatal:
// a torn tail is normal for a file being appended to concurrently, and
// abandoning the whole read over one bad line would discard the evidence.
func (s *txScan) line(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		s.st.Malformed++
		return
	}
	idx := s.st.Records
	s.st.Records++
	if e.Seq > s.st.MaxSeq {
		s.st.MaxSeq = e.Seq
	}
	if s.st.Records == 1 {
		s.st.FirstTS = e.TS
	}
	if !e.TS.IsZero() {
		s.st.LastTS = e.TS
	}
	s.lastKind = e.Kind
	switch e.Kind {
	case "complete":
		s.st.Completes++
		s.lastCompleteIdx = idx
	case "entry":
		s.entry(idx, e)
	}
}

func (s *txScan) entry(idx int, e envelope) {
	if e.Entry == nil {
		return
	}
	s.st.EntryRecords++
	s.types[e.Entry.Type] = true
	switch e.Entry.Type {
	case "assistant":
		s.st.AssistantEntries++
	case "tool_use":
		s.st.LastToolUse = e.TS
	case "user":
		s.lastUserIdx = idx
	}
	if e.Entry.Content != "" {
		key := [2]string{e.Entry.Type, e.Entry.Content}
		s.groups[key] = append(s.groups[key], e.TS)
	}
}

func (s *txScan) finish(thr Thresholds) TranscriptStat {
	st := s.st
	st.EntryTypes = sortedKeys(s.types)
	// Seq restarts at 0 per Recorder instance against an O_APPEND file, so
	// many records that never advance past 0 means many recorders — a
	// relaunch loop, not one conversation.
	st.SeqPinned = st.Records > 1 && st.MaxSeq == 0
	st.TurnClosed = s.lastCompleteIdx > s.lastUserIdx
	st.PendingPermission = s.lastKind == "permission"
	st.Redelivery = detectRedelivery(s.groups, thr)
	return s.applyTail(st)
}

// tailLine folds one line of the bounded suffix. Only last-record state is
// touched: nothing here may add to a count whose head the tail pass did not
// read.
func (s *txScan) tailLine(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return
	}
	t := s.tail
	idx := t.n
	t.n++
	if e.Seq > t.maxSeq {
		t.maxSeq = e.Seq
	}
	if !e.TS.IsZero() {
		t.lastTS = e.TS
	}
	t.lastKind = e.Kind
	switch e.Kind {
	case "complete":
		t.completeIdx = idx
	case "entry":
		t.entry(idx, e)
	}
}

func (t *txTail) entry(idx int, e envelope) {
	if e.Entry == nil {
		return
	}
	switch e.Entry.Type {
	case "tool_use":
		t.lastToolUse = e.TS
	case "user":
		t.userIdx = idx
	}
}

// applyTail overlays the bounded-suffix fold. A truncated file's head answers
// "how much" and its tail answers "where it ended"; keeping the head's
// last-record answers would be reporting the state of the 20000th record as
// the state of the run (U056-F02).
func (s *txScan) applyTail(st TranscriptStat) TranscriptStat {
	t := s.tail
	if t == nil {
		return st
	}
	if !t.lastTS.IsZero() {
		st.LastTS = t.lastTS
	}
	if t.maxSeq > st.MaxSeq {
		st.MaxSeq = t.maxSeq
		// A seq that advances anywhere in the file disproves the pinned-at-0
		// relaunch signature the head inferred from its own slice.
		st.SeqPinned = false
	}
	if !t.lastToolUse.IsZero() {
		st.LastToolUse = t.lastToolUse
	}
	if t.lastKind != "" {
		st.PendingPermission = t.lastKind == "permission"
	}
	// Only speak about the turn boundary if the tail actually saw one of its
	// two markers; otherwise the head's answer is still the best available.
	if t.completeIdx >= 0 || t.userIdx >= 0 {
		st.TurnClosed = t.completeIdx > t.userIdx
	}
	return st
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// detectRedelivery finds the worst repeated-content group in groups: identical
// (entry type, content) recorded at least thr.RedeliveryMinRepeats times, with
// consecutive gaps clustered tightly enough around their median to call it a
// CADENCE rather than a coincidence.
//
// Both conditions matter. Repetition alone is not a loop — a user can
// legitimately send the same short message twice, and a retry ladder can
// legitimately re-ask. Regularity alone is not a loop either — a polling agent
// emits regular but DIFFERENT records. Identical content on a metronome is
// what a machine re-delivering the same composed context looks like, and
// nothing else does.
func detectRedelivery(groups map[[2]string][]time.Time, thr Thresholds) *Redelivery {
	var best *Redelivery
	for key, ts := range groups {
		if len(ts) < thr.RedeliveryMinRepeats {
			continue
		}
		gaps := make([]time.Duration, 0, len(ts)-1)
		for i := 1; i < len(ts); i++ {
			d := ts[i].Sub(ts[i-1])
			if d < 0 {
				d = -d
			}
			gaps = append(gaps, d)
		}
		med := medianDuration(gaps)
		if med <= 0 {
			// Zero-gap repeats (same receipt timestamp, or no usable ts):
			// still repetition, but no cadence to speak of. Reported with a
			// zero cadence rather than skipped — a burst of identical records
			// is not healthier than a paced one.
			best = worse(best, &Redelivery{EntryType: key[0], Repeats: len(ts), Sample: sample(key[1])})
			continue
		}
		var maxDev time.Duration
		for _, g := range gaps {
			d := g - med
			if d < 0 {
				d = -d
			}
			if d > maxDev {
				maxDev = d
			}
		}
		if float64(maxDev) > thr.RedeliveryJitterRatio*float64(med) {
			continue // repeated, but not on a cadence
		}
		best = worse(best, &Redelivery{
			EntryType:    key[0],
			Repeats:      len(ts),
			Cadence:      med,
			MaxDeviation: maxDev,
			Sample:       sample(key[1]),
		})
	}
	return best
}

// worse keeps whichever redelivery group repeated more (ties broken by entry
// type for a stable, test-assertable answer).
func worse(a, b *Redelivery) *Redelivery {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Repeats > a.Repeats:
		return b
	case b.Repeats == a.Repeats && b.EntryType < a.EntryType:
		return b
	default:
		return a
	}
}

func medianDuration(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), d...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

func sample(s string) string {
	const n = 60
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// maxWalkEntries bounds NewestMTime's walk. A worktree is unbounded in
// principle and the monitor runs on a timer; a bounded, deterministic walk
// that reports what it saw beats an unbounded one that occasionally takes
// seconds.
const maxWalkEntries = 20000

// skipDirs are directories whose mtimes say nothing about whether the AGENT is
// working. .git churns on every internal git operation (including ones the
// monitor's own tooling causes), and dependency/artifact trees churn on
// builds that a stalled agent may still be running.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".cache":       true,
}

// NewestMTime reports the newest modification time under root, as filesystem
// evidence that an agent is touching its workspace. ok is false when root is
// empty, absent, or contained nothing countable — again a SIGNAL rather than
// an error, since a worktree with nothing in it is a statement about progress.
//
// A genuine walk error (permissions on root itself) IS returned: a failed
// observation must never be reported as an observation of stillness.
func NewestMTime(root string) (newest time.Time, ok bool, err error) {
	if root == "" {
		return time.Time{}, false, nil
	}
	info, statErr := os.Stat(root)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("liveness: stat workdir %s: %w", root, statErr)
	}
	if !info.IsDir() {
		return info.ModTime(), true, nil
	}
	seen := 0
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is not a verdict; keep walking
		}
		if seen >= maxWalkEntries {
			return fs.SkipAll
		}
		seen++
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if mt := fi.ModTime(); mt.After(newest) {
			newest = mt
			ok = true
		}
		return nil
	})
	if walkErr != nil {
		return newest, ok, fmt.Errorf("liveness: walk workdir %s: %w", root, walkErr)
	}
	return newest, ok, nil
}
