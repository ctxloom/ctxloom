package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/filelock"
)

// Event is one line in a per-project append-only task log (ADR 0025). The log
// at ~/.ctxloom/tasks/<project-id>.jsonl is the source of truth; current state
// is the fold of its events. Identity is the task harp; provenance is the
// `add` event's session.
type Event struct {
	Op       string    `json:"op"`                  // add | status | remove
	Task     string    `json:"task,omitempty"`      // task harp (identity)
	Text     string    `json:"text,omitempty"`      // add: task text
	Status   string    `json:"status,omitempty"`    // add/status: section name
	Trigger  string    `json:"trigger,omitempty"`   // add/status: revive condition for a Deferred task
	Session  string    `json:"session,omitempty"`   // origin (add) / acting (status,remove) session harp
	RepairOf string    `json:"repair_of,omitempty"` // add: anomaly key this re-add resolves
	Ts       time.Time `json:"ts"`
}

const (
	opAdd    = "add"
	opStatus = "status"
	opRemove = "remove"
)

// eventLog is the append-only backend behind a log-mode Store. It owns an
// in-process mutex and a cross-process advisory file lock; reads fold the log,
// mutations append a single line under both locks.
type eventLog struct {
	path    string
	session string // origin/acting session harp stamped on events
	mu      sync.Mutex
}

// folded is the in-memory projection of the log.
type folded struct {
	byID      map[string]*Task    // live tasks by harp
	order     []string            // add order of harp ids (for stable file-order)
	issued    map[string]struct{} // every harp ever used — never reused
	repaired  map[string]struct{} // anomaly keys already resolved by a re-add
	anomalies []Event             // displaced duplicate adds (same harp, different task)
}

func newFolded() *folded {
	return &folded{
		byID:     map[string]*Task{},
		issued:   map[string]struct{}{},
		repaired: map[string]struct{}{},
	}
}

// fold replays the log into current state. Malformed lines are skipped with a
// warning rather than failing the read (CLAUDE.md: never block task_list).
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
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: skipping malformed task event: %v\n", err)
			continue
		}
		f.apply(ev)
	}
	return f, nil
}

// apply folds a single event into state. Unknown ops are ignored (forward
// compatibility with logs written by a newer ctxloom).
func (f *folded) apply(ev Event) {
	switch ev.Op {
	case opAdd:
		if ev.RepairOf != "" {
			f.repaired[ev.RepairOf] = struct{}{}
		}
		if _, taken := f.issued[ev.Task]; taken {
			// Identity already used — a displaced duplicate (concurrent-mint
			// race, or the post-100-draw fallback). The first writer keeps the
			// harp; repair re-adds this content under a fresh one.
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
		t.Checked = statusIsDone(t.Status)
		t.TextHash = hashText(t.Text)
		f.byID[ev.Task] = t
		f.order = append(f.order, ev.Task)
	case opStatus:
		if t := f.byID[ev.Task]; t != nil {
			t.Status = ev.Status
			t.Checked = statusIsDone(ev.Status)
			// A status event only carries a trigger when one was (re)set; an
			// empty trigger never clears an existing condition.
			if tr := strings.TrimSpace(ev.Trigger); tr != "" {
				t.Trigger = tr
			}
		}
	case opRemove:
		delete(f.byID, ev.Task)
		// ev.Task stays in `issued`: a harp is never reused, so a stale
		// reference can never resolve to a different task.
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

// append writes one event as a single JSON line under O_APPEND. The caller
// holds the locks.
func (l *eventLog) append(ev Event) error {
	if ev.Ts.IsZero() {
		ev.Ts = time.Now().UTC()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (l *eventLog) lock() (func(), error) {
	l.mu.Lock()
	unlock, err := filelock.Lock(l.path + ".lock")
	if err != nil {
		l.mu.Unlock()
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() {
		unlock()
		l.mu.Unlock()
	}, nil
}

func (l *eventLog) add(text, status, trigger string) (Task, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Task{}, fmt.Errorf("text required")
	}
	if status == "" {
		status = StatusToDo
	}
	trigger = strings.TrimSpace(trigger)
	if err := ValidateStatusTrigger(status, trigger); err != nil {
		return Task{}, err
	}
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.fold()
	if err != nil {
		return Task{}, err
	}
	id := uniqueHarpIDFromSet(f.issued)
	if err := l.append(Event{Op: opAdd, Task: id, Text: text, Status: status, Trigger: trigger, Session: l.session}); err != nil {
		return Task{}, err
	}
	return Task{
		HarpID:        id,
		Text:          text,
		Status:        status,
		Checked:       statusIsDone(status),
		TextHash:      hashText(text),
		Trigger:       trigger,
		OriginSession: l.session,
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

	f, err := l.fold()
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
	if err := ValidateStatusTrigger(status, effective); err != nil {
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
	out.Checked = statusIsDone(status)
	out.Trigger = effective
	return out, nil
}

func (l *eventLog) remove(harpID string) (Task, error) {
	release, err := l.lock()
	if err != nil {
		return Task{}, err
	}
	defer release()

	f, err := l.fold()
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

func (l *eventLog) snapshot() ([]Task, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.fold()
	if err != nil {
		return nil, err
	}
	return f.taskList(), nil
}

func (l *eventLog) list(statuses []string, term string) ([]Task, error) {
	all, err := l.snapshot()
	if err != nil {
		return nil, err
	}
	return filterTasks(all, statuses, term), nil
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
		id := uniqueHarpIDFromSet(f.issued)
		f.issued[id] = struct{}{}
		f.repaired[key] = struct{}{}
		ev := Event{Op: opAdd, Task: id, Text: a.Text, Status: defaultStatus(a.Status), Session: a.Session, RepairOf: key}
		if err := l.append(ev); err != nil {
			return err
		}
	}
	return nil
}

// importTasks seeds the log with pre-existing tasks (e.g. migrated markdown),
// preserving each task's harp identity and origin. Tasks whose harp is already
// issued are skipped, so import is safe to call on a non-empty log. Each lands
// as a single `add` carrying its status.
func (l *eventLog) importTasks(in []Task) error {
	release, err := l.lock()
	if err != nil {
		return err
	}
	defer release()

	f, err := l.fold()
	if err != nil {
		return err
	}
	for _, t := range in {
		if t.HarpID == "" {
			continue
		}
		if _, taken := f.issued[t.HarpID]; taken {
			continue
		}
		f.issued[t.HarpID] = struct{}{}
		ev := Event{
			Op:      opAdd,
			Task:    t.HarpID,
			Text:    strings.TrimSpace(t.Text),
			Status:  defaultStatus(t.Status),
			Trigger: strings.TrimSpace(t.Trigger),
			Session: t.OriginSession,
		}
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
