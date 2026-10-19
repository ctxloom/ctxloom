package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// PruneRequest names which tasks a prune takes and whether it may write.
type PruneRequest struct {
	// Statuses are the statuses that qualify a task: literal names, with any
	// @-class already expanded (ExpandStatusClasses). At least one is
	// required — an empty set is refused rather than read as "every status",
	// the reading every list filter gives it and the worst possible one for
	// a command that deletes.
	Statuses []string
	// Before bounds the age: a task qualifies only when its last status
	// change — the add that set its first status, or its latest status event
	// — is strictly before this instant. The zero time is refused for the
	// same reason an empty Statuses is.
	Before time.Time
	// TagQuery narrows the selection with the postfix grammar `taskloom list
	// --tag-query` takes. Empty applies no tag filter; a malformed one is an
	// error wrapping ErrTagQuery.
	TagQuery string
	// Schema feeds the tag query's type comparison, exactly as for
	// Store.ListWithTagQuery; nil is fine.
	Schema *tagschema.Schema
	// Apply rewrites the log. Without it the prune reports what it would
	// take and leaves the log untouched.
	Apply bool
}

// PrunedTask is one task a prune took, or without Apply would take: enough to
// recognise it (the CompactTask fields) plus the status-change time that put
// it past the bound.
type PrunedTask struct {
	HarpID           string    `json:"harp_id"`
	Status           string    `json:"status"`
	Tags             []string  `json:"tags"`
	Headline         string    `json:"headline"`
	LastStatusChange time.Time `json:"last_status_change"`
}

// DanglingReference is one place a surviving task still names a pruned one.
// Via says where: "tag <tag>" for a tag carrying the harp (the relates:<harp>
// convention), "text" or "trigger" for the harp written as a word in prose.
type DanglingReference struct {
	HarpID string `json:"harp_id"` // the surviving task holding the reference
	Pruned string `json:"pruned"`  // the pruned harp it names
	Via    string `json:"via"`
}

// PruneResult is what a prune selected and what removing it costs the log.
// The counts describe the rewrite whether or not it was applied, so a
// report-only run states exactly what --yes would do.
type PruneResult struct {
	Applied        bool                `json:"applied"`
	Pruned         []PrunedTask        `json:"pruned"`
	TasksPruned    int                 `json:"tasks_pruned"`
	EventsRemoved  int                 `json:"events_removed"`
	TasksRemaining int                 `json:"tasks_remaining"`
	Dangling       []DanglingReference `json:"dangling_references"`
}

// Prune drops old finished tasks from the log: every event of each task req
// selects is removed, and every other line is kept byte for byte, so each
// surviving task folds exactly as it did before. The rewrite is atomic (a
// temp file renamed over the log) and runs under the log's exclusive lock, so
// a concurrent add or status change either lands before the prune reads the
// log or waits and lands in the rewritten one.
//
// Without req.Apply nothing is written. A pruned harp is no longer recorded
// anywhere in the log, so — unlike a removed task's — it is no longer
// reserved against being minted again.
func (s *Store) Prune(req PruneRequest) (PruneResult, error) {
	return s.log.prune(req)
}

// replaceLog installs a prune's rewritten log: safefs.WriteFileKeepMode,
// indirected so a test can put a concurrent writer into the window between the
// prune's read and its rename — the one interleaving the write lock exists to
// rule out, and one a real scheduler will not produce on demand.
var replaceLog = safefs.WriteFileKeepMode

func (l *eventLog) prune(req PruneRequest) (PruneResult, error) {
	if len(req.Statuses) == 0 {
		return PruneResult{}, errors.New("prune needs at least one status to select by: an empty status set is never read as every status")
	}
	if req.Before.IsZero() {
		return PruneResult{}, errors.New("prune needs an age bound: a zero bound is never read as every age")
	}
	none := PruneResult{Applied: req.Apply, Pruned: []PrunedTask{}, Dangling: []DanglingReference{}}
	// Absent means nothing to prune, and taking a lock to learn that would
	// create the lock file as a side effect of looking (see readFolded).
	if _, err := os.Stat(l.path); errors.Is(err, os.ErrNotExist) {
		return none, nil
	}
	release, err := l.pruneLock(req.Apply)
	if err != nil {
		return PruneResult{}, err
	}
	defer release()

	data, err := os.ReadFile(l.path)
	if err != nil {
		return PruneResult{}, err
	}
	before, err := l.foldData(data)
	if err != nil {
		return PruneResult{}, err
	}
	if err := before.anomalyError(l.path); err != nil {
		return PruneResult{}, err
	}
	selected, err := before.selectForPrune(req)
	if err != nil {
		return PruneResult{}, err
	}
	pruned := make(map[string]struct{}, len(selected))
	for _, t := range selected {
		pruned[t.HarpID] = struct{}{}
	}
	plan, err := planPrune(data, pruned)
	if err != nil {
		return PruneResult{}, fmt.Errorf("%s: %w", l.path, err)
	}
	after, err := l.foldData(plan.kept)
	if err != nil {
		return PruneResult{}, fmt.Errorf("%s: refusing to prune, nothing was written: the rewritten log does not fold: %w", l.path, err)
	}
	if err := checkSurvivors(before, after, pruned); err != nil {
		return PruneResult{}, fmt.Errorf("%s: refusing to prune, nothing was written: %w", l.path, err)
	}

	survivors := after.taskList()
	res := none
	res.TasksPruned = len(selected)
	res.EventsRemoved = plan.removed
	res.TasksRemaining = len(survivors)
	for _, t := range selected {
		res.Pruned = append(res.Pruned, PrunedTask{
			HarpID:           t.HarpID,
			Status:           t.Status,
			Tags:             t.Tags,
			Headline:         Headline(t.Text),
			LastStatusChange: before.statusSetAt[t.HarpID],
		})
	}
	res.Dangling = danglingReferences(survivors, pruned)
	if !req.Apply || len(selected) == 0 {
		return res, nil
	}
	// Durable: appends after this one land in the NEW file and fsync only
	// that file, so a rename whose directory entry reverted after a power
	// loss would take every one of them with it. AllowEmpty: checkSurvivors
	// has just shown an empty result is what pruning every task folds to.
	if err := replaceLog(safefs.New().Fs, l.path, plan.kept, "task log", safefs.Durable(), safefs.AllowEmpty()); err != nil {
		return PruneResult{}, err
	}
	return res, nil
}

// pruneLock takes the lock a prune needs: the exclusive write lock when it
// will rewrite the log, so no append can land between the read it filters
// and the rename that replaces it; when it only reports, the shared lock
// every read takes.
func (l *eventLog) pruneLock(apply bool) (release func(), err error) {
	if apply {
		return l.lock()
	}
	l.mu.Lock()
	unlockShared := l.lockShared()
	return func() {
		unlockShared()
		l.mu.Unlock()
	}, nil
}

// selectForPrune is a prune's selection over the fold: the live tasks in one
// of req.Statuses that match req.TagQuery and whose last status change is
// before req.Before, in add order.
func (f *folded) selectForPrune(req PruneRequest) ([]Task, error) {
	candidates, err := filterTasks(f.taskList(), req.Statuses, "", req.TagQuery, req.Schema)
	if err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(candidates))
	for _, t := range candidates {
		if f.statusSetAt[t.HarpID].Before(req.Before) {
			out = append(out, t)
		}
	}
	return out, nil
}

// prunePlan is the log a prune would install and how many lines it drops.
type prunePlan struct {
	kept    []byte
	removed int
}

// planPrune filters data down to the lines that do not belong to a pruned
// task. A kept line is kept byte for byte, position and duplicates included,
// so the fold's ordering (ts, then file order for ties) sees the survivors
// exactly as before.
//
// A line belongs to a pruned task when it addresses the task's harp, or when
// it is the displaced add the task was re-added from by Store.Repair, named
// by the re-add's repair_of. That add is the pruned task's own creation under
// a harp it lost; left behind, it would turn a resolved collision back into
// one that fails every read.
func planPrune(data []byte, pruned map[string]struct{}) (prunePlan, error) {
	lines := strings.SplitAfter(string(data), "\n")
	events := make([]*Event, len(lines))
	repairedFrom := map[string]struct{}{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
			return prunePlan{}, fmt.Errorf("line %d: malformed task event: %w", i+1, err)
		}
		events[i] = &ev
		if _, gone := pruned[ev.Task]; gone && ev.Op == opAdd && ev.RepairOf != "" {
			repairedFrom[ev.RepairOf] = struct{}{}
		}
	}
	var plan prunePlan
	var kept strings.Builder
	for i, line := range lines {
		if ev := events[i]; ev != nil && belongsToPruned(*ev, pruned, repairedFrom) {
			plan.removed++
			continue
		}
		kept.WriteString(line)
	}
	plan.kept = []byte(kept.String())
	return plan, nil
}

func belongsToPruned(ev Event, pruned, repairedFrom map[string]struct{}) bool {
	if _, gone := pruned[ev.Task]; gone {
		return true
	}
	if ev.Op != opAdd || len(repairedFrom) == 0 {
		return false
	}
	_, gone := repairedFrom[anomalyKey(ev)]
	return gone
}

// checkSurvivors holds a prune to its contract before anything is written:
// the rewritten log must fold every surviving task exactly as the old one did
// — the same task, in the same add order, with the same status-change and
// Deferred times — and must carry no collision the old one had resolved.
// planPrune is meant to guarantee this; checking the result makes it a
// property of every prune rather than an argument about the filter.
func checkSurvivors(before, after *folded, pruned map[string]struct{}) error {
	want := make([]Task, 0, len(before.byID))
	for _, t := range before.taskList() {
		if _, gone := pruned[t.HarpID]; !gone {
			want = append(want, t)
		}
	}
	got := after.taskList()
	if len(got) != len(want) {
		return fmt.Errorf("the rewritten log folds %d surviving task(s) where the old one folds %d", len(got), len(want))
	}
	for i := range want {
		id := want[i].HarpID
		if !sameTask(want[i], got[i]) {
			return fmt.Errorf("the rewritten log would change surviving task %s", id)
		}
		if !before.statusSetAt[id].Equal(after.statusSetAt[id]) || !before.deferredSince[id].Equal(after.deferredSince[id]) {
			return fmt.Errorf("the rewritten log would change when surviving task %s last changed status", id)
		}
	}
	return after.anomalyError("the rewritten log")
}

// sameTask is field-for-field equality with CreatedAt compared as an instant:
// two folds of the same line parse equal times, but a timestamp written with
// a zone offset parses to a fresh *time.Location each time, which
// reflect.DeepEqual would call different.
func sameTask(a, b Task) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return false
	}
	a.CreatedAt, b.CreatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// danglingReferences lists every place a surviving task names a pruned one: a
// tag with the harp as its key or value (relates:<harp>), or the harp as a
// whole word of its text or trigger. Prune has no policy for rewriting another
// task's tags or prose, so it reports them and leaves them alone.
func danglingReferences(survivors []Task, pruned map[string]struct{}) []DanglingReference {
	out := []DanglingReference{}
	for _, t := range survivors {
		note := func(s, via string) {
			seen := map[string]struct{}{}
			for _, word := range harpWords(s) {
				if _, gone := pruned[word]; !gone {
					continue
				}
				if _, dup := seen[word]; dup {
					continue
				}
				seen[word] = struct{}{}
				out = append(out, DanglingReference{HarpID: t.HarpID, Pruned: word, Via: via})
			}
		}
		for _, tag := range t.Tags {
			note(tag, "tag "+tag)
		}
		note(t.Text, "text")
		note(t.Trigger, "trigger")
	}
	return out
}

// harpWords splits s into the runs of characters a harp is spelled with
// (lowercase letters, digits and hyphens), so a harp is matched as a whole
// word and never as part of a longer hyphenated one.
func harpWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
	})
}
