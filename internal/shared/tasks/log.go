package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// Event is one line in a per-project append-only task log (ADR 0025). The log
// at ~/.ctxloom/tasks/<project-id>.jsonl is the source of truth; current state
// is the fold of its events. Identity is the task harp; provenance is the
// `add` event's session.
type Event struct {
	Op       string    `json:"op"`                  // add | status | text | remove | tag | untag
	Task     string    `json:"task,omitempty"`      // task harp (identity)
	Text     string    `json:"text,omitempty"`      // add: task text
	Status   string    `json:"status,omitempty"`    // add/status: section name
	Trigger  string    `json:"trigger,omitempty"`   // add/status: revive condition for a Deferred task
	Session  string    `json:"session,omitempty"`   // origin (add) / acting (status,remove,tag,untag) session harp
	RepairOf string    `json:"repair_of,omitempty"` // add: anomaly key this re-add resolves
	Tags     []string  `json:"tags,omitempty"`      // add: initial tags / tag: tags to union / untag: tags to subtract
	Ts       time.Time `json:"ts"`
}

const (
	opAdd    = "add"
	opStatus = "status"
	opText   = "text"
	opRemove = "remove"
	// opTag and opUntag were added in the tag-query log-format bump: they
	// union/subtract Event.Tags onto a task's derived tag set. A log carrying
	// them is rejected by any pre-tag taskloom binary (apply's fail-loud
	// unknown-op rule) — that binary must be upgraded, not the log downgraded.
	opTag   = "tag"
	opUntag = "untag"
)

// eventLog is the append-only backend behind a Store. It owns an in-process
// mutex and a cross-process advisory file lock; reads fold the log, mutations
// append a single line under both locks.
type eventLog struct {
	path    string
	session string // origin/acting session harp stamped on events
	mu      sync.Mutex
}

// folded is the in-memory projection of the log.
type folded struct {
	byID      map[string]*Task    // live tasks by harp
	order     []string            // harp ids in replay order (ts-sorted; file order only breaks ties)
	issued    map[string]struct{} // every harp ever used — never reused
	repaired  map[string]struct{} // anomaly keys already resolved by a re-add
	anomalies []Event             // displaced duplicate adds (same harp, different task)

	// deferredSince tracks, per harp, the Ts of the event that most recently
	// moved it into Deferred status — updated on every add/status event that
	// sets Deferred, so a re-deferral after a revive overwrites the earlier
	// value. Trigger evaluation reads this (via Store.DeferredSince) to scope
	// evidence to what happened AFTER a task was parked, not the whole
	// project history. Left stale (not deleted) on a move OUT of Deferred; the
	// public accessor filters to only currently-Deferred tasks, so a stale
	// entry for a non-Deferred task is simply never surfaced.
	deferredSince map[string]time.Time
}

func newFolded() *folded {
	return &folded{
		byID:          map[string]*Task{},
		issued:        map[string]struct{}{},
		repaired:      map[string]struct{}{},
		deferredSince: map[string]time.Time{},
	}
}

// parsedEvent pairs a decoded event with the 1-based line number it came
// from, so an apply-time error can still name the original file location
// after events have been reordered for replay.
type parsedEvent struct {
	ev     Event
	lineNo int
}

// fold replays the log into current state. The log is the sole record of a
// project's outstanding work (ADR 0025): per CLAUDE.md's fail-loud philosophy,
// a reader must never silently drop a record, because a silently dropped task
// is exactly the lost-deferral failure the task system exists to prevent. So a
// malformed line, or an event this reader can't interpret, fails the whole
// fold — no partial view is ever returned.
//
// The log is checked into git with a `merge=union` driver (ADR 0025 follow-
// on): a union merge concatenates both sides of a conflicting hunk — ours
// then theirs — with no conflict markers and no dedup. That breaks two
// assumptions a naive line-by-line fold would make, so this method fixes
// both:
//
//  1. File order is no longer a valid proxy for happens-before. Events are
//     buffered and replayed in `ts` order (stable sort, so equal timestamps
//     keep their original file-order relative position) instead of raw file
//     order. KNOWN LIMIT: ts is wall-clock from whichever machine wrote the
//     record, so cross-machine clock skew can still mis-order concurrent
//     events. That is bounded by the skew between the machines involved —
//     strictly better than "whatever git happened to concatenate," but not
//     a substitute for a real logical clock. Written down here rather than
//     left implicit, per this project's workaround-discipline norm.
//  2. A union merge does not deduplicate, so a byte-identical line can
//     appear twice (both branches wrote the exact same record). Each raw
//     line is hashed as it's read; an exact repeat is skipped before it
//     ever reaches apply() — most importantly for a duplicate `add`, which
//     would otherwise register as a harp collision (see apply's identity
//     guard) even though it is a harmless merge artifact, not a real
//     collision.
//
// A REAL identity collision — two different tasks independently minted the
// same harp on separate branches — is a distinct problem this fold does not
// and cannot silently fix (global uniqueness isn't derivable from two
// partitioned writers' local state). apply() still records it as an anomaly;
// callers that need current live state (snapshot, deferredSince) surface it
// as a loud, actionable error instead of silently dropping the second task.
// Holds no lock — callers serialize as needed.
func (l *eventLog) fold() (*folded, error) {
	f := newFolded()
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	lineNo := 0
	seenLines := map[string]struct{}{}
	parsed := make([]parsedEvent, 0)
	for line := range strings.SplitSeq(string(data), "\n") {
		lineNo++
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Union-merge dedup: a byte-identical line appearing twice is a
		// merge artifact, not a new event — skip it before it's even
		// parsed, so it can never register as a duplicate-add anomaly.
		if _, dup := seenLines[line]; dup {
			continue
		}
		seenLines[line] = struct{}{}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("%s:%d: malformed task event: %w — inspect/repair the line, or move the file aside and re-add tasks", l.path, lineNo, err)
		}
		parsed = append(parsed, parsedEvent{ev: ev, lineNo: lineNo})
	}
	// Chronological replay: stable sort by ts. A stable sort preserves the
	// original (file) order for equal timestamps, which is the documented
	// tiebreak.
	sort.SliceStable(parsed, func(i, j int) bool {
		return parsed[i].ev.Ts.Before(parsed[j].ev.Ts)
	})
	for _, p := range parsed {
		if err := f.apply(p.ev); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", l.path, p.lineNo, err)
		}
	}
	return f, nil
}

// foldChecked is fold() plus the anomaly gate every read and write other
// than repair() itself must apply. Previously only snapshot()/
// deferredSince() checked anomalyError after folding — every mutator
// (add, addTags, removeTags, setStatus, setText, remove) and currentTags
// called fold() directly, so an unresolved harp collision made every read
// fail loud while every write kept right on succeeding into a store
// nothing could read. repair() is the one caller that must see
// the raw anomalies, so it keeps calling fold() directly.
func (l *eventLog) foldChecked() (*folded, error) {
	f, err := l.fold()
	if err != nil {
		return nil, err
	}
	if err := f.anomalyError(l.path); err != nil {
		return nil, err
	}
	return f, nil
}

// apply folds a single event into state. An unrecognized op is a fatal fold
// error, not silently ignored: the project rejects silent forward-compat
// record dropping (see CLAUDE.md; no-backward-compat-shims policy applies in
// reverse here too) — a log carrying an op this binary doesn't know was very
// likely written by a newer taskloom, and the fix is to upgrade, not to lose
// the event.
func (f *folded) apply(ev Event) error {
	switch ev.Op {
	case opAdd:
		f.applyAdd(ev)
	case opTag:
		f.retagLive(ev, unionTags)
	case opUntag:
		f.retagLive(ev, subtractTags)
	case opStatus:
		f.applyStatus(ev)
	case opText:
		f.applyText(ev)
	case opRemove:
		// ev.Task stays in `issued`: a harp is never reused, so a stale
		// reference can never resolve to a different task.
		delete(f.byID, ev.Task)
	case "":
		return fmt.Errorf("record has no op field")
	default:
		return fmt.Errorf("unrecognized op %q — this log was likely written by a newer taskloom; upgrade this binary", ev.Op)
	}
	return nil
}

// applyAdd mints a task, or records a harp collision as an anomaly when the
// identity is already claimed.
func (f *folded) applyAdd(ev Event) {
	if ev.RepairOf != "" {
		f.repaired[ev.RepairOf] = struct{}{}
	}
	if _, taken := f.issued[ev.Task]; taken {
		// Identity already used — a displaced duplicate (concurrent-mint
		// race, the post-100-draw fallback, or two branches that each
		// independently minted this harp, later union-merged). Byte-
		// identical duplicate lines never reach here — fold() dedupes
		// those before apply() runs. So an event that DOES land here is
		// a real collision: two different tasks claiming one identity.
		// The first writer keeps the harp; this content is recorded as
		// an anomaly rather than silently dropped or auto-repaired
		// (reassigning a harp would break every reference to it).
		// snapshot()/deferredSince() surface unresolved anomalies as a
		// loud, actionable error (anomalyError) rather than letting the
		// second task vanish with a clean exit code; a human runs
		// Store.Repair() to re-add it under a fresh harp.
		f.anomalies = append(f.anomalies, ev)
		return
	}
	f.issued[ev.Task] = struct{}{}
	t := &Task{
		HarpID:        ev.Task,
		Text:          strings.TrimSpace(ev.Text),
		Status:        defaultStatus(ev.Status),
		Trigger:       strings.TrimSpace(ev.Trigger),
		OriginSession: ev.Session,
	}
	t.Checked = IsTerminalStatus(t.Status)
	t.TextHash = hashText(t.Text)
	t.Tags = normalizeTags(ev.Tags)
	t.CreatedAt = ev.Ts
	f.byID[ev.Task] = t
	f.order = append(f.order, ev.Task)
	f.noteIfDeferred(ev, t.Status)
}

// retagLive applies a tag-set delta (union for `tag`, subtract for `untag`)
// to a live task. An event addressed to a task that is not in byID — never
// added, or already removed — applies to nothing; see
// TestApply_EventsForAnUnknownHarpAreSilentlyDropped for what that costs and
// why changing it is a persisted-format decision, not this function's.
func (f *folded) retagLive(ev Event, delta func(base, arg []string) []string) {
	if t := f.byID[ev.Task]; t != nil {
		t.Tags = delta(t.Tags, ev.Tags)
	}
}

func (f *folded) applyStatus(ev Event) {
	t := f.byID[ev.Task]
	if t == nil {
		return
	}
	t.Status = ev.Status
	t.Checked = IsTerminalStatus(ev.Status)
	// A status event only carries a trigger when one was (re)set; an
	// empty trigger never clears an existing condition.
	if tr := strings.TrimSpace(ev.Trigger); tr != "" {
		t.Trigger = tr
	}
	f.noteIfDeferred(ev, ev.Status)
}

func (f *folded) applyText(ev Event) {
	if t := f.byID[ev.Task]; t != nil {
		t.Text = strings.TrimSpace(ev.Text)
		t.TextHash = hashText(t.Text)
	}
}

// noteIfDeferred records when a task most recently entered Deferred, the one
// fact both add and status contribute to deferredSince.
func (f *folded) noteIfDeferred(ev Event, status string) {
	if status == StatusDeferred {
		f.deferredSince[ev.Task] = ev.Ts
	}
}

// taskList returns live tasks in add order.
func (f *folded) taskList() []Task {
	out := make([]Task, 0, len(f.byID))
	for _, id := range f.order {
		if t := f.byID[id]; t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// anomalyError returns a loud, actionable error if this fold contains any
// UNRESOLVED harp collision — an `add` for a harp already claimed by a
// different task (see apply's identity guard). "Unresolved" excludes
// collisions a completed Store.Repair() has already re-added under a fresh
// harp (tracked via RepairOf/anomalyKey): those are done, and must not keep
// failing every read forever after being fixed.
//
// This is deliberately NOT called from fold() or repair() itself — repair()
// needs the raw anomaly list to do its job, so it calls fold() directly and
// bypasses this check. It IS called from the read paths (snapshot,
// deferredSince) that a human or the CLI actually consults for current
// state: those must fail loud rather than silently return a view with a
// task missing and another corrupted.
func (f *folded) anomalyError(path string) error {
	var unresolved []Event
	for _, a := range f.anomalies {
		if _, done := f.repaired[anomalyKey(a)]; !done {
			unresolved = append(unresolved, a)
		}
	}
	if len(unresolved) == 0 {
		return nil
	}
	descs := make([]string, 0, len(unresolved))
	for _, a := range unresolved {
		kept := "(no current task — since removed)"
		if t := f.byID[a.Task]; t != nil {
			kept = fmt.Sprintf("kept task text %q", t.Text)
		}
		descs = append(descs, fmt.Sprintf("harp %q: %s, displaced task text %q (session %q)", a.Task, kept, a.Text, a.Session))
	}
	return fmt.Errorf("%s: unresolved harp collision(s) — two different tasks were independently minted with the same harp id (most likely two branches, each correct against what it could see, later union-merged); the displaced task is NOT lost but is excluded from this view, and every event addressed to its harp is silently applying to the OTHER task instead. This cannot be auto-repaired (reassigning a harp would break every plan file and cross-reference pointing at it) — inspect and run `taskloom repair` (or Store.Repair() directly) to re-add the displaced task(s) under a fresh harp: %s", path, strings.Join(descs, "; "))
}

// append writes one event as a single JSON line under O_APPEND. The caller
// holds the locks.
func (l *eventLog) append(ev Event) error {
	if err := admissible(ev); err != nil {
		return err
	}
	if ev.Ts.IsZero() {
		ev.Ts = time.Now().UTC()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, statErr := os.Stat(l.path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if created {
		// The log file did not exist a moment ago, so this open just created
		// its directory entry — and appendLine's fsync below makes the file's
		// CONTENTS durable, never the entry that names them. Flush the entry
		// first, so a power loss can lose the whole file or nothing, never
		// leave a confirmed event in a file with no name. Done BEFORE the
		// event is written so a failure here is unambiguous: nothing was
		// appended, and the caller's error means exactly that.
		//
		// iox.SyncDir, not the Durable() Option: this append is O_APPEND onto
		// a file that may already exist, never an atomic rename, so
		// WriteFileAtomic's Option shape does not fit here — only the raw
		// directory-fsync primitive underneath it does.
		if err := iox.SyncDir(dir); err != nil {
			return closeAfter(f, fmt.Errorf("sync task log directory %s: %w", dir, err))
		}
	}
	return closeAfter(f, appendLine(f, append(b, '\n')))
}

// closeAfter closes f and decides which error the caller should see. opErr
// wins when it is non-nil: it is the diagnosis (a failed write, a failed
// sync), and the close error that follows a failure is noise. When the
// operation succeeded, the close error IS the result — this is the log's only
// write path, and a close failure means the event the caller is about to be
// told it wrote may not be there. Discarding it would report success for a
// write that did not land.
func closeAfter(f *os.File, opErr error) error {
	cerr := f.Close()
	if opErr != nil {
		return opErr
	}
	return cerr
}

// admissible rejects an event this reader could never fold back. The write
// seam is where such a record must be stopped: fold() treats an unreadable
// record as fatal for the WHOLE log, so a single inadmissible line does not
// cost one task, it costs the store — every later read and write fails until
// a human edits the file. Only invariants every op shares are enforced here
// (an op this binary knows, and an identity to address), never per-op
// payload rules, which belong to the mutators that own them. This is a
// write-time check by design and must never move onto the fold path: an
// existing log already carrying such a line has to keep loading, exactly as
// the reader stays lenient about tag shape (see tagsToTagmaTags).
func admissible(ev Event) error {
	switch ev.Op {
	case opAdd, opStatus, opText, opRemove, opTag, opUntag:
	case "":
		return fmt.Errorf("refusing to write a task event with no op field: it would make every later read of this log fail")
	default:
		return fmt.Errorf("refusing to write a task event with unrecognized op %q", ev.Op)
	}
	if strings.TrimSpace(ev.Task) == "" {
		return fmt.Errorf("refusing to write a %q event with no task id: every event addresses a task by harp", ev.Op)
	}
	return nil
}

// appendLine writes line to f — already opened O_APPEND, so the write always
// lands at the file's current end — and fsyncs it before returning success.
// If either the write or the sync fails partway (ENOSPC, EIO, a killed
// process resumed with a stale fd, ...), it rolls the file back to its
// length as of just before this call rather than leaving a torn line on
// disk. That matters because fold() treats ANY unparseable line as fatal
// for the whole log — without this, a single failed append from
// THIS process would brick every future read and write of the store, with
// "move the file aside and re-add every task" as the only stated recovery.
// Truncate's own error is deliberately ignored: it is already best-effort
// (the original Write/Sync error is what the caller needs to see), and if
// the filesystem is in a state where even Truncate fails, there is nothing
// more this function can do about it.
func appendLine(f *os.File, line []byte) error {
	off, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, werr := f.Write(line); werr != nil {
		_ = f.Truncate(off)
		return werr
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Truncate(off)
		return serr
	}
	return nil
}

// lockPath is the single home of this log's lock-file name. Every acquisition
// — the exclusive write lock and all three shared read locks — goes through
// here, so they cannot name different files and quietly stop excluding one
// another.
func (l *eventLog) lockPath() string { return filelock.PathFor(l.path) }

func (l *eventLog) lock() (func(), error) {
	l.mu.Lock()
	unlock, err := filelock.Lock(l.lockPath())
	if err != nil {
		l.mu.Unlock()
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() {
		unlock()
		l.mu.Unlock()
	}, nil
}

// addWithTags is the sole append path for a new task. It stamps an initial
// tag set on the `add` event itself (rather than a follow-on `tag` event),
// so a task's creation and its starting tags land as one atomic log line;
// Store.AddWithTrigger (which has no tags to add) calls it with a nil set.
func (l *eventLog) addWithTags(text, status, trigger string, tags []string) (Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Task{}, fmt.Errorf("text required")
	}
	if status == "" {
		status = StatusToDo
	}
	trigger = strings.TrimSpace(trigger)
	if err := validateStatusTrigger(status, trigger); err != nil {
		return Task{}, err
	}
	tags = normalizeTags(tags)
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.foldChecked()
	if err != nil {
		return Task{}, err
	}
	id, err := uniqueHarpIDFromSet(f.issued)
	if err != nil {
		return Task{}, err
	}
	// Stamped explicitly (rather than left zero for append() to default)
	// so the Ts actually written matches the CreatedAt returned below —
	// otherwise a caller reading the just-added task's CreatedAt back would
	// see a zero value until the next fold.
	now := time.Now().UTC()
	if err := l.append(Event{Op: opAdd, Task: id, Text: text, Status: status, Trigger: trigger, Session: l.session, Tags: tags, Ts: now}); err != nil {
		return Task{}, err
	}
	return Task{
		HarpID:        id,
		Text:          text,
		Status:        status,
		Checked:       IsTerminalStatus(status),
		TextHash:      hashText(text),
		Trigger:       trigger,
		OriginSession: l.session,
		Tags:          tags,
		CreatedAt:     now,
	}, nil
}

func (l *eventLog) setStatus(harpID, status, trigger string) (Task, error) {
	if status == "" {
		return Task{}, fmt.Errorf("status required")
	}
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.foldChecked()
	if err != nil {
		return Task{}, err
	}
	t := f.byID[harpID]
	if t == nil {
		return Task{}, fmt.Errorf("task not found: %s", harpID)
	}
	// A non-empty trigger updates the condition; otherwise the task keeps the
	// one it already had, so re-deferring needs no re-typing.
	effective := effectiveTrigger(trigger, t.Trigger)
	if err := validateStatusTrigger(status, effective); err != nil {
		return Task{}, err
	}
	// Persist the trigger on the event only when it changed, so a plain status
	// move stays a minimal record and apply's empty-trigger rule is preserved.
	evTrigger := ""
	if effective != t.Trigger {
		evTrigger = effective
	}
	if err := l.append(Event{Op: opStatus, Task: harpID, Status: status, Trigger: evTrigger, Session: l.session}); err != nil {
		return Task{}, err
	}
	out := *t
	out.Status = status
	out.Checked = IsTerminalStatus(status)
	out.Trigger = effective
	return out, nil
}

func (l *eventLog) setText(harpID, text string) (Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Task{}, fmt.Errorf("text required")
	}
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.foldChecked()
	if err != nil {
		return Task{}, err
	}
	t := f.byID[harpID]
	if t == nil {
		return Task{}, fmt.Errorf("task not found: %s", harpID)
	}
	if err := l.append(Event{Op: opText, Task: harpID, Text: text, Session: l.session}); err != nil {
		return Task{}, err
	}
	out := *t
	out.Text = text
	out.TextHash = hashText(text)
	return out, nil
}

func (l *eventLog) remove(harpID string) (Task, error) {
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.foldChecked()
	if err != nil {
		return Task{}, err
	}
	t := f.byID[harpID]
	if t == nil {
		return Task{}, fmt.Errorf("task not found: %s", harpID)
	}
	if err := l.append(Event{Op: opRemove, Task: harpID, Session: l.session}); err != nil {
		return Task{}, err
	}
	return *t, nil
}

// addTags unions tags onto harpID's current tag set and returns the updated
// task. At least one (non-empty, post-trim) tag is required — an empty call
// is rejected rather than silently appending a no-op event.
func (l *eventLog) addTags(harpID string, tags []string) (Task, error) {
	tags = normalizeTags(tags)
	if len(tags) == 0 {
		return Task{}, fmt.Errorf("at least one tag required")
	}
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.foldChecked()
	if err != nil {
		return Task{}, err
	}
	t := f.byID[harpID]
	if t == nil {
		return Task{}, fmt.Errorf("task not found: %s", harpID)
	}
	if err := l.append(Event{Op: opTag, Task: harpID, Tags: tags, Session: l.session}); err != nil {
		return Task{}, err
	}
	out := *t
	out.Tags = unionTags(t.Tags, tags)
	return out, nil
}

// removeTags subtracts tags from harpID's current tag set and returns the
// updated task. Removing a tag the task doesn't have is a no-op for that tag
// (not an error); at least one tag argument is still required.
func (l *eventLog) removeTags(harpID string, tags []string) (Task, error) {
	tags = normalizeTags(tags)
	if len(tags) == 0 {
		return Task{}, fmt.Errorf("at least one tag required")
	}
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.foldChecked()
	if err != nil {
		return Task{}, err
	}
	t := f.byID[harpID]
	if t == nil {
		return Task{}, fmt.Errorf("task not found: %s", harpID)
	}
	if err := l.append(Event{Op: opUntag, Task: harpID, Tags: tags, Session: l.session}); err != nil {
		return Task{}, err
	}
	out := *t
	out.Tags = subtractTags(t.Tags, tags)
	return out, nil
}

// currentTags returns harpID's current (folded) tag set — a read-only
// lookup, never appending anything. Mirrors snapshot()'s shared-lock
// pattern: a SHARED cross-process lock so a concurrent mutator's in-flight
// append (held under the EXCLUSIVE lock) can never be observed
// half-written; best-effort, since a read must never block on a lock
// failure.
func (l *eventLog) currentTags(harpID string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if unlock, err := filelock.LockShared(l.lockPath()); err == nil {
		defer unlock()
	}
	f, err := l.foldChecked()
	if err != nil {
		return nil, err
	}
	t := f.byID[harpID]
	if t == nil {
		return nil, fmt.Errorf("task not found: %s", harpID)
	}
	return t.Tags, nil
}

func (l *eventLog) snapshot() ([]Task, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Take a SHARED cross-process lock for the read. Mutators hold the EXCLUSIVE
	// lock while appending (see lock()), so without this a fold could observe a
	// partially written final line from another process. fold() treats any
	// unparseable line as fatal for the WHOLE log, so that observation surfaces
	// as a transient hard read failure hiding every task in the store — not as
	// one silently missing task. Best-effort: a lock failure falls back to
	// an unlocked read rather than failing, since reads must never block (the
	// in-process mu still serializes same-process access).
	if unlock, err := filelock.LockShared(l.lockPath()); err == nil {
		defer unlock()
	}
	f, err := l.foldChecked()
	if err != nil {
		return nil, err
	}
	return f.taskList(), nil
}

// deferredSince returns, for every currently Deferred task, the timestamp of
// the event that most recently moved it into Deferred status. Locking mirrors
// snapshot(): a shared cross-process lock for the read, best-effort (a lock
// failure falls back to unlocked rather than blocking a read).
func (l *eventLog) deferredSince() (map[string]time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if unlock, err := filelock.LockShared(l.lockPath()); err == nil {
		defer unlock()
	}
	f, err := l.foldChecked()
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(f.deferredSince))
	for id, ts := range f.deferredSince {
		if t := f.byID[id]; t != nil && t.Status == StatusDeferred {
			out[id] = ts
		}
	}
	return out, nil
}

// listWithTagQuery filters the store's tasks by status/term and an
// additional postfix tag-query filter (see filterTasks, backed by tagma).
// An empty tagQuery behaves like a plain status/term list; a malformed one
// surfaces as an error, never a silently empty or unfiltered result. schema
// feeds filterTasks's client-loadable type comparison bridging (tagma
// SPEC.md §9); nil is fine (no type config contributed) — Store.List passes
// "" and nil through unconditionally for exactly that case.
func (l *eventLog) listWithTagQuery(statuses []string, term, tagQuery string, schema *tagschema.Schema) ([]Task, error) {
	all, err := l.snapshot()
	if err != nil {
		return nil, err
	}
	return filterTasks(all, statuses, term, tagQuery, schema)
}

func (l *eventLog) summarize() (Summary, error) {
	all, err := l.snapshot()
	if err != nil {
		return Summary{}, err
	}
	out := Summary{Counts: map[string]int{}}
	for _, t := range all {
		out.Counts[t.Status]++
		if t.Status == StatusInProgress {
			out.InProgress = append(out.InProgress, t.HarpID)
		}
	}
	return out, nil
}

// repair re-introduces any displaced duplicate-add (an identity collision the
// fold detected) under a fresh harp, idempotently: each re-add carries the
// anomaly's key so a later fold won't repair it twice. A no-op on a clean log.
func (l *eventLog) repair() error {
	release, err := l.lock()
	if err != nil {
		return err
	}
	defer release()

	f, err := l.fold()
	if err != nil {
		return err
	}
	for _, a := range f.anomalies {
		key := anomalyKey(a)
		if _, done := f.repaired[key]; done {
			continue
		}
		id, err := uniqueHarpIDFromSet(f.issued)
		if err != nil {
			return err
		}
		f.issued[id] = struct{}{}
		f.repaired[key] = struct{}{}
		// Carry the displaced add's trigger through the repair: a Deferred task
		// re-added without its trigger would violate the Deferred invariant
		// (validateStatusTrigger). repair() appends directly, bypassing that
		// validation, so guard here — fall a malformed anomaly back to a plain
		// to-do rather than re-introduce the forbidden state or drop the task.
		status, trigger := defaultStatus(a.Status), a.Trigger
		if err := validateStatusTrigger(status, trigger); err != nil {
			status, trigger = StatusToDo, ""
		}
		// Carry Tags and Ts through too: the displaced task's own
		// fields, not the repair's own defaults. Dropping Tags silently
		// discards payload despite reporting success; dropping Ts (the
		// original add's timestamp) makes append() re-stamp the CURRENT
		// time as CreatedAt, silently pinning the recovered task to the
		// extreme of any age-based decay_fn (see priority.Compute's own
		// CreatedAt.IsZero guard) instead of preserving its real
		// age.
		ev := Event{Op: opAdd, Task: id, Text: a.Text, Status: status, Trigger: trigger, Session: a.Session, Tags: a.Tags, Ts: a.Ts, RepairOf: key}
		if err := l.append(ev); err != nil {
			return err
		}
	}
	return nil
}

func defaultStatus(s string) string {
	if s == "" {
		return StatusToDo
	}
	return s
}

// anomalyKey is a stable identity for a displaced duplicate-add, so its repair
// can be recorded and never repeated. The timestamp disambiguates otherwise
// identical adds.
func anomalyKey(ev Event) string {
	return hashText(ev.Task + "\x00" + ev.Text + "\x00" + ev.Session + "\x00" + ev.Ts.UTC().Format(time.RFC3339Nano))
}
