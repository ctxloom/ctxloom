package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// pruneNow is the instant every prune test measures ages from. Events carry
// explicit timestamps relative to it, so "8 days old" is a fact of the log,
// never of the wall clock the test happens to run at.
var pruneNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return pruneNow.Add(-time.Duration(n) * 24 * time.Hour) }

// writeEvents seeds a fresh log at a temp path with evs, one JSON line each,
// in the order given, and opens a Store over it.
func writeEvents(t *testing.T, evs ...Event) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	writeEventsAt(t, path, evs...)
	s, err := OpenLog(path, "")
	require.NoError(t, err)
	return s
}

func writeEventsAt(t *testing.T, path string, evs ...Event) {
	t.Helper()
	var b strings.Builder
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		require.NoError(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

func addEv(harp, status string, ts time.Time, tags ...string) Event {
	return Event{Op: opAdd, Task: harp, Text: "task " + harp, Status: status, Tags: tags, Ts: ts}
}

func statusEv(harp, to string, ts time.Time) Event {
	return Event{Op: opStatus, Task: harp, Status: to, Ts: ts}
}

func prunedHarps(r PruneResult) []string {
	out := make([]string, 0, len(r.Pruned))
	for _, p := range r.Pruned {
		out = append(out, p.HarpID)
	}
	sort.Strings(out)
	return out
}

// defaultRequest is the selection `taskloom prune` makes with no flags: Done
// tasks whose last status change is more than seven days before now.
func defaultRequest() PruneRequest {
	return PruneRequest{Statuses: []string{StatusDone}, Before: daysAgo(7)}
}

// The population the selection tests share: one task for each way a task can
// fall on either side of the default rule.
func selectionLog(t *testing.T) *Store {
	t.Helper()
	return writeEvents(t,
		// Done eight days ago: selected.
		addEv("old-done", StatusToDo, daysAgo(40)),
		statusEv("old-done", StatusDone, daysAgo(8)),
		// Done six days ago: inside the week, kept.
		addEv("fresh-done", StatusToDo, daysAgo(40)),
		statusEv("fresh-done", StatusDone, daysAgo(6)),
		// Open for a month: old, but not finished.
		addEv("old-todo", StatusToDo, daysAgo(30)),
		// Closed a month ago, reopened, and closed again two days ago: the
		// LAST status change is what counts, so it is kept.
		addEv("reclosed", StatusToDo, daysAgo(40)),
		statusEv("reclosed", StatusDone, daysAgo(30)),
		statusEv("reclosed", StatusInProgress, daysAgo(3)),
		statusEv("reclosed", StatusDone, daysAgo(2)),
		// Added already Done ten days ago: the add IS its last status change.
		addEv("born-done", StatusDone, daysAgo(10)),
		// Done twenty days ago and retagged yesterday: a tag is not a status
		// change, so it is still selected.
		addEv("retagged", StatusToDo, daysAgo(40)),
		statusEv("retagged", StatusDone, daysAgo(20)),
		Event{Op: opTag, Task: "retagged", Tags: []string{"late"}, Ts: daysAgo(1)},
		// Archived long ago: terminal, but not Done.
		addEv("old-archived", StatusToDo, daysAgo(40)),
		statusEv("old-archived", StatusArchived, daysAgo(20)),
	)
}

func TestPrune_DefaultSelectsDoneTasksWhoseLastStatusChangePrecedesTheBound(t *testing.T) {
	s := selectionLog(t)

	res, err := s.Prune(defaultRequest())
	require.NoError(t, err)

	assert.Equal(t, []string{"born-done", "old-done", "retagged"}, prunedHarps(res),
		"exactly the Done tasks whose LAST status change is older than the bound")
}

func TestPrune_ReportsEachSelectedTasksLastStatusChange(t *testing.T) {
	s := selectionLog(t)

	res, err := s.Prune(defaultRequest())
	require.NoError(t, err)

	got := map[string]time.Time{}
	for _, p := range res.Pruned {
		got[p.HarpID] = p.LastStatusChange
	}
	assert.True(t, got["old-done"].Equal(daysAgo(8)), "old-done: %v", got["old-done"])
	assert.True(t, got["born-done"].Equal(daysAgo(10)), "born-done: the add is its last status change, %v", got["born-done"])
	assert.True(t, got["retagged"].Equal(daysAgo(20)), "retagged: a tag event is not a status change, %v", got["retagged"])
}

func TestPrune_TheBoundMovesTheSelection(t *testing.T) {
	s := selectionLog(t)

	wider := defaultRequest()
	wider.Before = daysAgo(5)
	res, err := s.Prune(wider)
	require.NoError(t, err)
	assert.Equal(t, []string{"born-done", "fresh-done", "old-done", "retagged"}, prunedHarps(res),
		"a five-day bound also takes the task closed six days ago")

	narrower := defaultRequest()
	narrower.Before = daysAgo(9)
	res, err = s.Prune(narrower)
	require.NoError(t, err)
	assert.Equal(t, []string{"born-done", "retagged"}, prunedHarps(res),
		"a nine-day bound leaves the task closed eight days ago")
}

func TestPrune_StatusesNameWhatQualifies(t *testing.T) {
	s := selectionLog(t)

	for _, tc := range []struct {
		name     string
		statuses []string
		want     []string
	}{
		{"one other status", []string{StatusArchived}, []string{"old-archived"}},
		{"several statuses", []string{StatusDone, StatusArchived}, []string{"born-done", "old-archived", "old-done", "retagged"}},
		{"a non-terminal status", []string{StatusToDo}, []string{"old-todo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := defaultRequest()
			req.Statuses = tc.statuses
			res, err := s.Prune(req)
			require.NoError(t, err)
			assert.Equal(t, tc.want, prunedHarps(res))
		})
	}
}

func TestPrune_TagQueryNarrowsTheSelection(t *testing.T) {
	s := writeEvents(t,
		addEv("tagged", StatusDone, daysAgo(10), "release"),
		addEv("untagged", StatusDone, daysAgo(10)),
		addEv("other", StatusDone, daysAgo(10), "docs"),
	)

	req := defaultRequest()
	req.TagQuery = "release"
	res, err := s.Prune(req)
	require.NoError(t, err)
	assert.Equal(t, []string{"tagged"}, prunedHarps(res))

	req.TagQuery = "release/not"
	res, err = s.Prune(req)
	require.NoError(t, err)
	assert.Equal(t, []string{"other", "untagged"}, prunedHarps(res), "the postfix grammar list --tag-query takes")

	req.TagQuery = "release/and"
	_, err = s.Prune(req)
	require.ErrorIs(t, err, ErrTagQuery, "a malformed query fails loud, never selects everything or nothing")
}

func TestPrune_ReportOnlyLeavesTheLogByteIdentical(t *testing.T) {
	s := selectionLog(t)
	before, err := os.ReadFile(s.Path())
	require.NoError(t, err)

	res, err := s.Prune(defaultRequest())
	require.NoError(t, err)
	require.NotEmpty(t, res.Pruned, "precondition: there is something a --yes run would prune")
	assert.False(t, res.Applied)

	after, err := os.ReadFile(s.Path())
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a report-only prune must not touch the log")
	assertNoTempFiles(t, filepath.Dir(s.Path()))
}

func TestPrune_ApplyRemovesEveryEventOfThePrunedTasksAndNothingElse(t *testing.T) {
	s := writeEvents(t,
		// Pruned, with every kind of event a task can carry.
		addEv("gone", StatusToDo, daysAgo(40), "a", "b"),
		Event{Op: opTag, Task: "gone", Tags: []string{"c"}, Ts: daysAgo(39)},
		Event{Op: opUntag, Task: "gone", Tags: []string{"a"}, Ts: daysAgo(38)},
		Event{Op: opText, Task: "gone", Text: "rewritten", Ts: daysAgo(37)},
		statusEv("gone", StatusDeferred, daysAgo(36)),
		statusEv("gone", StatusDone, daysAgo(30)),
		// Survivors exercising every fold rule: tags, untag, text, a
		// Deferred trigger, and a status history.
		addEv("kept-deferred", StatusToDo, daysAgo(40), "x"),
		Event{Op: opUntag, Task: "kept-deferred", Tags: []string{"x"}, Ts: daysAgo(35)},
		Event{Op: opText, Task: "kept-deferred", Text: "kept text", Ts: daysAgo(34)},
		Event{Op: opStatus, Task: "kept-deferred", Status: StatusDeferred, Trigger: "the v2 API ships", Ts: daysAgo(33)},
		addEv("kept-open", StatusToDo, daysAgo(20), "y"),
		Event{Op: opTag, Task: "kept-open", Tags: []string{"z"}, Ts: daysAgo(19)},
		addEv("kept-fresh", StatusToDo, daysAgo(20)),
		statusEv("kept-fresh", StatusDone, daysAgo(1)),
		// A tombstoned task's events are not a live task's: left alone.
		addEv("tombstoned", StatusToDo, daysAgo(50)),
		Event{Op: opRemove, Task: "tombstoned", Ts: daysAgo(49)},
	)
	wantTasks, err := s.Snapshot()
	require.NoError(t, err)
	wantTasks = without(wantTasks, "gone")
	wantDeferred, err := s.DeferredSince()
	require.NoError(t, err)

	req := defaultRequest()
	req.Apply = true
	res, err := s.Prune(req)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, []string{"gone"}, prunedHarps(res))
	assert.Equal(t, 6, res.EventsRemoved)
	assert.Equal(t, 1, res.TasksPruned)
	assert.Equal(t, 3, res.TasksRemaining)

	raw, err := os.ReadFile(s.Path())
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev Event
		require.NoError(t, json.Unmarshal([]byte(line), &ev))
		assert.NotEqual(t, "gone", ev.Task, "an event of a pruned task survived: %s", line)
	}
	assert.Contains(t, string(raw), `"task":"tombstoned"`, "a tombstoned task is not a live Done task; prune does not take it")

	gotTasks, err := s.Snapshot()
	require.NoError(t, err)
	assert.Equal(t, wantTasks, gotTasks, "every survivor folds exactly as it did before the prune")
	gotDeferred, err := s.DeferredSince()
	require.NoError(t, err)
	assert.Equal(t, wantDeferred, gotDeferred)
	assertNoTempFiles(t, filepath.Dir(s.Path()))
}

func without(list []Task, harp string) []Task {
	out := make([]Task, 0, len(list))
	for _, t := range list {
		if t.HarpID != harp {
			out = append(out, t)
		}
	}
	return out
}

// A union merge can leave a byte-identical line twice. The survivor's copies
// stay exactly as they were (the fold dedups them either way); the pruned
// task's copies all go.
func TestPrune_ApplyKeepsSurvivorsLinesVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	kept := `{"op":"add","task":"kept","text":"kept","status":"To Do","ts":"2026-09-01T00:00:00Z"}`
	gone := `{"op":"add","task":"gone","text":"gone","status":"Done","ts":"2026-09-01T00:00:00Z"}`
	raw := kept + "\n" + gone + "\n" + kept + "\n" + gone + "\n"
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o644))
	s, err := OpenLog(path, "")
	require.NoError(t, err)

	req := defaultRequest()
	req.Apply = true
	res, err := s.Prune(req)
	require.NoError(t, err)
	assert.Equal(t, 2, res.EventsRemoved)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, kept+"\n"+kept+"\n", string(after))
}

func TestPrune_ApplyWithNothingSelectedLeavesTheLogUntouched(t *testing.T) {
	s := writeEvents(t, addEv("fresh", StatusDone, daysAgo(1)))
	before, err := os.ReadFile(s.Path())
	require.NoError(t, err)
	info, err := os.Stat(s.Path())
	require.NoError(t, err)

	req := defaultRequest()
	req.Apply = true
	res, err := s.Prune(req)
	require.NoError(t, err)
	assert.Empty(t, res.Pruned)

	after, err := os.ReadFile(s.Path())
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	again, err := os.Stat(s.Path())
	require.NoError(t, err)
	assert.True(t, os.SameFile(info, again), "nothing to prune must not replace the file")
}

func TestPrune_ApplyCanEmptyTheLog(t *testing.T) {
	s := writeEvents(t, addEv("only", StatusDone, daysAgo(10)))

	req := defaultRequest()
	req.Apply = true
	_, err := s.Prune(req)
	require.NoError(t, err)

	raw, err := os.ReadFile(s.Path())
	require.NoError(t, err)
	assert.Empty(t, raw)
	list, err := s.Snapshot()
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestPrune_ApplyKeepsTheLogsMode(t *testing.T) {
	s := writeEvents(t, addEv("gone", StatusDone, daysAgo(10)), addEv("kept", StatusToDo, daysAgo(10)))
	require.NoError(t, os.Chmod(s.Path(), 0o600))

	req := defaultRequest()
	req.Apply = true
	_, err := s.Prune(req)
	require.NoError(t, err)

	info, err := os.Stat(s.Path())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "rewriting the log must not widen who can read it")
}

func TestPrune_AnAbsentLogHasNothingToPruneAndIsNotCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "taskloom.jsonl")
	s, err := OpenLog(path, "")
	require.NoError(t, err)

	req := defaultRequest()
	req.Apply = true
	res, err := s.Prune(req)
	require.NoError(t, err)
	assert.Empty(t, res.Pruned)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "looking for old tasks must not create the log or its lock")
}

func TestPrune_RefusesARequestThatSelectsByNothing(t *testing.T) {
	s := selectionLog(t)

	noStatus := defaultRequest()
	noStatus.Statuses = nil
	_, err := s.Prune(noStatus)
	require.Error(t, err, "no status must never mean every status")

	noBound := defaultRequest()
	noBound.Before = time.Time{}
	_, err = s.Prune(noBound)
	require.Error(t, err, "no bound must never mean every age")
}

// A surviving task that names a pruned one keeps naming it: prune has no
// policy for rewriting someone else's tag or prose, so it reports each such
// reference instead and prunes regardless.
func TestPrune_ReportsSurvivorsThatReferenceAPrunedTask(t *testing.T) {
	s := writeEvents(t,
		addEv("gone-task", StatusDone, daysAgo(10)),
		addEv("by-tag", StatusToDo, daysAgo(5), "relates:gone-task"),
		Event{Op: opAdd, Task: "by-text", Text: "follow-up to gone-task, see there", Status: StatusToDo, Ts: daysAgo(5)},
		Event{Op: opAdd, Task: "by-trigger", Text: "parked", Status: StatusDeferred, Trigger: "gone-task ships", Ts: daysAgo(5)},
		Event{Op: opAdd, Task: "near-miss", Text: "about long-gone-task-x, not the pruned one", Status: StatusToDo, Ts: daysAgo(5)},
	)

	req := defaultRequest()
	req.Apply = true
	res, err := s.Prune(req)
	require.NoError(t, err)
	assert.Equal(t, []string{"gone-task"}, prunedHarps(res), "a reference does not stop the prune")

	assert.ElementsMatch(t, []DanglingReference{
		{HarpID: "by-tag", Pruned: "gone-task", Via: "tag relates:gone-task"},
		{HarpID: "by-text", Pruned: "gone-task", Via: "text"},
		{HarpID: "by-trigger", Pruned: "gone-task", Via: "trigger"},
	}, res.Dangling)
}

// Store.Repair links a re-added task to the collision it resolves through the
// re-add's repair_of. Pruning either side must leave the other readable: the
// displaced add belongs to the task re-added from it.
func TestPrune_RespectsTheRepairLinkOnEitherSide(t *testing.T) {
	collision := func(t *testing.T) *Store {
		t.Helper()
		displaced := Event{Op: opAdd, Task: "alpha", Text: "displaced writer", Status: StatusDone, Ts: daysAgo(30)}
		return writeEvents(t,
			Event{Op: opAdd, Task: "alpha", Text: "first writer", Status: StatusToDo, Ts: daysAgo(31)},
			displaced,
			Event{Op: opAdd, Task: "beta", Text: "displaced writer", Status: StatusDone, Ts: daysAgo(30), RepairOf: anomalyKey(displaced)},
		)
	}

	t.Run("pruning the re-added task", func(t *testing.T) {
		s := collision(t)
		req := defaultRequest()
		req.Apply = true
		res, err := s.Prune(req)
		require.NoError(t, err)
		assert.Equal(t, []string{"beta"}, prunedHarps(res))
		list, err := s.Snapshot()
		require.NoError(t, err, "the collision beta resolved must not come back unresolved")
		require.Len(t, list, 1)
		assert.Equal(t, "first writer", list[0].Text)
	})

	t.Run("pruning the task that kept the harp", func(t *testing.T) {
		s := collision(t)
		require.NoError(t, os.WriteFile(s.Path(), append(mustRead(t, s.Path()),
			[]byte(`{"op":"status","task":"alpha","status":"Done","ts":"`+daysAgo(20).Format(time.RFC3339Nano)+`"}`+"\n")...), 0o644))
		req := defaultRequest()
		req.Apply = true
		res, err := s.Prune(req)
		require.NoError(t, err)
		assert.Equal(t, []string{"alpha", "beta"}, prunedHarps(res))
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// An unresolved collision fails every read, and a prune is a read first.
func TestPrune_RefusesALogWithAnUnresolvedCollision(t *testing.T) {
	s := writeEvents(t,
		Event{Op: opAdd, Task: "alpha", Text: "first", Status: StatusDone, Ts: daysAgo(30)},
		Event{Op: opAdd, Task: "alpha", Text: "second", Status: StatusDone, Ts: daysAgo(29)},
	)
	before := mustRead(t, s.Path())

	req := defaultRequest()
	req.Apply = true
	_, err := s.Prune(req)
	require.Error(t, err)
	assert.Equal(t, string(before), string(mustRead(t, s.Path())))
}

// contentionSignal stands in a writer's lock: each exclusive take first tries
// once, and when the lock is already held it closes contended before queueing
// for it. That is the observable fact the concurrency test needs — the writer
// really arrived while someone held the log — without waiting on a clock.
type contentionSignal struct {
	safefs.Locks
	contended chan struct{}
}

func (c contentionSignal) Lock(path string) (safefs.Lock, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lk, err := c.TryLock(ctx, path)
	if err == nil {
		return lk, nil
	}
	if !errors.Is(err, safefs.ErrLockHeld) {
		return nil, err
	}
	close(c.contended)
	return c.Locks.Lock(path)
}

// A writer that arrives while a prune is between reading the log and
// replacing it must not be lost. The writer is forced into exactly that
// window: the prune's replace step starts it and does not proceed until the
// writer has either queued behind the prune's lock or — if no lock held it
// off — finished appending to the file about to be replaced.
func TestPrune_AWriterArrivingMidPruneWaitsAndItsEventSurvives(t *testing.T) {
	pruner := writeEvents(t,
		addEv("gone", StatusDone, daysAgo(10)),
		addEv("kept", StatusToDo, daysAgo(10)),
	)
	// A second Store over the same file: its own in-process mutex, so only
	// the cross-process lock orders it against the prune, as for another
	// taskloom process.
	writer, err := OpenLog(pruner.Path(), "writer")
	require.NoError(t, err)
	contended := make(chan struct{})
	writer.log.locks = contentionSignal{Locks: writer.log.locks, contended: contended}

	type added struct {
		task Task
		err  error
	}
	done := make(chan added, 1)
	real := replaceLog
	t.Cleanup(func() { replaceLog = real })
	replaceLog = func(fs afero.Fs, path string, data []byte, desc string, opts ...safefs.Option) error {
		go func() {
			task, err := writer.AddWithTrigger("filed while the prune ran", "", "")
			done <- added{task, err}
		}()
		select {
		case <-contended:
		case a := <-done:
			done <- a
		}
		return real(fs, path, data, desc, opts...)
	}

	req := defaultRequest()
	req.Apply = true
	res, err := pruner.Prune(req)
	require.NoError(t, err)
	require.Equal(t, []string{"gone"}, prunedHarps(res))

	a := <-done
	require.NoError(t, a.err)
	list, err := pruner.Snapshot()
	require.NoError(t, err)
	var harps []string
	for _, task := range list {
		harps = append(harps, task.HarpID)
	}
	assert.ElementsMatch(t, []string{"kept", a.task.HarpID}, harps,
		"the event appended during the prune must survive it, and the pruned task must stay gone")
}

// checkSurvivors is the prune's last word before it writes: a rewrite whose
// fold disagrees with the old one about any surviving task is refused.
func TestCheckSurvivors_RefusesARewriteThatChangesASurvivor(t *testing.T) {
	before := newFolded()
	require.NoError(t, before.apply(addEv("kept", StatusToDo, daysAgo(10), "x")))
	require.NoError(t, before.apply(addEv("gone", StatusDone, daysAgo(10))))
	pruned := map[string]struct{}{"gone": {}}

	faithful := newFolded()
	require.NoError(t, faithful.apply(addEv("kept", StatusToDo, daysAgo(10), "x")))
	require.NoError(t, checkSurvivors(before, faithful, pruned))

	lostTag := newFolded()
	require.NoError(t, lostTag.apply(addEv("kept", StatusToDo, daysAgo(10))))
	require.Error(t, checkSurvivors(before, lostTag, pruned))

	lostTask := newFolded()
	require.Error(t, checkSurvivors(before, lostTask, pruned))
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"), "a temp file was left behind: %s", e.Name())
	}
}
