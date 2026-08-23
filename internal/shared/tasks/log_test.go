package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func newLog(t *testing.T, session string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	s, err := OpenLog(path, session)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	return s
}

func TestLogAddListRoundTrip(t *testing.T) {
	s := newLog(t, "swift-amber-falcon")

	a, err := s.AddWithTrigger("write the storage layer", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if a.Status != StatusToDo {
		t.Fatalf("status = %q, want %q", a.Status, StatusToDo)
	}
	if a.OriginSession != "swift-amber-falcon" {
		t.Fatalf("origin = %q, want the session harp", a.OriginSession)
	}

	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].HarpID != a.HarpID || got[0].Text != "write the storage layer" {
		t.Fatalf("list = %+v", got)
	}
	if got[0].OriginSession != "swift-amber-falcon" {
		t.Fatalf("origin not folded: %q", got[0].OriginSession)
	}
}

// TestLogAddWithTagsFoldsInitialTags pins the `add`-carries-tags path: a
// task's creation and its starting tags land as one atomic log line, and the
// fold surfaces them sorted+deduped.
func TestLogAddWithTagsFoldsInitialTags(t *testing.T) {
	s := newLog(t, "")
	a, err := s.AddWithTags("write the storage layer", "", "", "beta", "alpha", "beta")
	if err != nil {
		t.Fatalf("add with tags: %v", err)
	}
	if len(a.Tags) != 2 || a.Tags[0] != "alpha" || a.Tags[1] != "beta" {
		t.Fatalf("Tags = %v, want [alpha beta] (sorted, deduped)", a.Tags)
	}

	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || len(got[0].Tags) != 2 || got[0].Tags[0] != "alpha" || got[0].Tags[1] != "beta" {
		t.Fatalf("folded tags = %+v", got)
	}
}

// TestLogAddTagsUnionsAndIsIdempotent covers the `tag` fold rule: it unions
// onto the current set, and re-adding an already-present tag changes nothing.
func TestLogAddTagsUnionsAndIsIdempotent(t *testing.T) {
	s := newLog(t, "")
	a, _ := s.AddWithTrigger("ship it", "", "")

	got, err := s.AddTags(a.HarpID, "urgent")
	if err != nil {
		t.Fatalf("add tags: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "urgent" {
		t.Fatalf("Tags = %v, want [urgent]", got.Tags)
	}

	got, err = s.AddTags(a.HarpID, "release", "urgent")
	if err != nil {
		t.Fatalf("add tags 2: %v", err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "release" || got.Tags[1] != "urgent" {
		t.Fatalf("Tags after union = %v, want [release urgent]", got.Tags)
	}

	// Idempotent: unioning the same tags again is a no-op on the tag set.
	got, err = s.AddTags(a.HarpID, "urgent")
	if err != nil {
		t.Fatalf("add tags 3 (idempotent): %v", err)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("re-adding an existing tag should not duplicate: %v", got.Tags)
	}
}

// TestLogCurrentTags covers the read-only current-tags lookup the
// operations write seam uses (scalar-collapse) to see a task's tags BEFORE
// mutating them: it reflects the same folded state List/Snapshot would,
// without appending anything, and errors on an unknown harp id exactly like
// the other harp-keyed lookups (AddTags/RemoveTags).
func TestLogCurrentTags(t *testing.T) {
	s := newLog(t, "")
	a, err := s.AddWithTags("ship it", "", "", "urgent")
	if err != nil {
		t.Fatalf("add with tags: %v", err)
	}

	got, err := s.CurrentTags(a.HarpID)
	if err != nil {
		t.Fatalf("current tags: %v", err)
	}
	if len(got) != 1 || got[0] != "urgent" {
		t.Fatalf("CurrentTags = %v, want [urgent]", got)
	}

	if _, err := s.AddTags(a.HarpID, "release"); err != nil {
		t.Fatalf("add tags: %v", err)
	}
	got, err = s.CurrentTags(a.HarpID)
	if err != nil {
		t.Fatalf("current tags after union: %v", err)
	}
	if len(got) != 2 || got[0] != "release" || got[1] != "urgent" {
		t.Fatalf("CurrentTags after union = %v, want [release urgent]", got)
	}

	if _, err := s.CurrentTags("no-such-harp"); err == nil {
		t.Fatal("expected an error for an unknown harp id")
	}
}

// TestLogRemoveTagsSubtractsAndAbsentIsNoop covers the `untag` fold rule:
// subtraction from the current set, and removing a tag the task never had is
// a no-op rather than an error.
func TestLogRemoveTagsSubtractsAndAbsentIsNoop(t *testing.T) {
	s := newLog(t, "")
	a, err := s.AddWithTags("ship it", "", "", "urgent", "release")
	if err != nil {
		t.Fatalf("add with tags: %v", err)
	}

	got, err := s.RemoveTags(a.HarpID, "urgent")
	if err != nil {
		t.Fatalf("remove tags: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "release" {
		t.Fatalf("Tags after remove = %v, want [release]", got.Tags)
	}

	// Removing an absent tag is a no-op, not an error.
	got, err = s.RemoveTags(a.HarpID, "nonexistent")
	if err != nil {
		t.Fatalf("remove absent tag should not error: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "release" {
		t.Fatalf("Tags after no-op remove = %v, want unchanged [release]", got.Tags)
	}
}

func TestLogAddTagsUnknownHarpErrors(t *testing.T) {
	s := newLog(t, "")
	if _, err := s.AddTags("no-such-id", "urgent"); err == nil {
		t.Fatal("expected error tagging an unknown task")
	}
	if _, err := s.RemoveTags("no-such-id", "urgent"); err == nil {
		t.Fatal("expected error untagging an unknown task")
	}
}

// TestLogAddTagsRequiresAtLeastOneTag pins the fail-loud contract: an empty
// tag call is rejected outright rather than silently appending a no-op event.
func TestLogAddTagsRequiresAtLeastOneTag(t *testing.T) {
	s := newLog(t, "")
	a, _ := s.AddWithTrigger("ship it", "", "")
	if _, err := s.AddTags(a.HarpID); err == nil {
		t.Fatal("expected error adding zero tags")
	}
	if _, err := s.RemoveTags(a.HarpID); err == nil {
		t.Fatal("expected error removing zero tags")
	}
}

// TestLogTagsSurviveReFold is the append-only proof at unit scale: tags
// applied through one Store handle are visible when a SECOND Store handle
// opens the same on-disk log and re-folds it from scratch — nothing but the
// event stream carries the state.
func TestLogTagsSurviveReFold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	s1, err := OpenLog(path, "")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	a, err := s1.AddWithTags("ship it", "", "", "urgent")
	if err != nil {
		t.Fatalf("add with tags: %v", err)
	}
	if _, err := s1.AddTags(a.HarpID, "release"); err != nil {
		t.Fatalf("add tags: %v", err)
	}
	if _, err := s1.RemoveTags(a.HarpID, "urgent"); err != nil {
		t.Fatalf("remove tags: %v", err)
	}

	s2, err := OpenLog(path, "")
	if err != nil {
		t.Fatalf("re-open log: %v", err)
	}
	got, err := s2.List(nil, "")
	if err != nil {
		t.Fatalf("list from fresh handle: %v", err)
	}
	if len(got) != 1 || len(got[0].Tags) != 1 || got[0].Tags[0] != "release" {
		t.Fatalf("re-folded tags = %+v, want just [release]", got)
	}
}

// TestLogTaglessLogFoldsUnchanged is the no-regression proof: a log that
// never carries a tag/untag event, nor an add with tags, folds to the exact
// same task shape it did before tags existed (Tags empty/nil, everything
// else untouched).
func TestLogTaglessLogFoldsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"pre-tags task","status":"To Do","session":"swift-amber-falcon","ts":"2026-01-01T00:00:00Z"}
{"op":"status","task":"alpha","status":"In Progress","ts":"2026-01-01T00:00:01Z"}
{"op":"text","task":"alpha","text":"renamed","ts":"2026-01-01T00:00:02Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenLog(path, "")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d: %+v", len(got), got)
	}
	task := got[0]
	if task.HarpID != "alpha" || task.Text != "renamed" || task.Status != StatusInProgress {
		t.Fatalf("fold regressed: %+v", task)
	}
	if task.OriginSession != "swift-amber-falcon" {
		t.Fatalf("origin session regressed: %q", task.OriginSession)
	}
	if len(task.Tags) != 0 {
		t.Fatalf("a tag-less log must fold to an empty tag set, got %v", task.Tags)
	}
}

// TestLogFoldToleratesPreExistingUnqueryableTags is the read-path-must-not-
// validate guarantee: an existing log written before the write-seam guard
// existed (or written by some other means) can carry a tag containing "/" or
// a reserved operator word ("and") — both of which operations.validateTag
// now rejects at write time. Folding that log must still succeed and surface
// the tag verbatim; the reader degrades gracefully rather than failing to
// load a task just because one of its tags is now unqueryable.
func TestLogFoldToleratesPreExistingUnqueryableTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"pre-guard task","status":"To Do","session":"swift-amber-falcon","tags":["foo/bar","and","urgent"],"ts":"2026-01-01T00:00:00Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenLog(path, "")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list must not fail on a pre-existing unqueryable tag: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 task, got %d: %+v", len(got), got)
	}
	task := got[0]
	want := []string{"and", "foo/bar", "urgent"}
	if !slices.Equal(task.Tags, want) {
		t.Fatalf("Tags = %v, want %v (normalizeTags sorts/dedupes but must not validate)", task.Tags, want)
	}
}

func TestLogSetStatusLastWriteWins(t *testing.T) {
	s := newLog(t, "")
	a, _ := s.AddWithTrigger("ship it", "", "")

	if _, err := s.SetStatusWithTrigger(a.HarpID, StatusInProgress, ""); err != nil {
		t.Fatalf("status1: %v", err)
	}
	if _, err := s.SetStatusWithTrigger(a.HarpID, StatusDone, ""); err != nil {
		t.Fatalf("status2: %v", err)
	}

	got, _ := s.List(nil, "")
	if len(got) != 1 || got[0].Status != StatusDone || !got[0].Checked {
		t.Fatalf("want Done+checked, got %+v", got)
	}
}

// TestLogAddSetsCreatedAtAndFoldPreservesIt pins the task's creation
// timestamp: Add's returned Task already carries a non-zero CreatedAt (the
// same instant stamped on the `add` event, not left for a later fold to
// backfill), and a subsequent status/tag change never touches it —
// internal/shared/tasks/priority reads it to derive a task's age, so it must
// stay pinned to creation, not the most recent event.
func TestLogAddSetsCreatedAtAndFoldPreservesIt(t *testing.T) {
	s := newLog(t, "")
	a, err := s.AddWithTrigger("ship it", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if a.CreatedAt.IsZero() {
		t.Fatal("Add's returned Task must already carry a non-zero CreatedAt")
	}

	if _, err := s.SetStatusWithTrigger(a.HarpID, StatusInProgress, ""); err != nil {
		t.Fatalf("status: %v", err)
	}
	if _, err := s.AddTags(a.HarpID, "urgent"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || !got[0].CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("CreatedAt must survive a status/tag change unchanged: got %+v, want %v", got, a.CreatedAt)
	}
}

func TestLogSetStatusUnknownErrors(t *testing.T) {
	s := newLog(t, "")
	if _, err := s.SetStatusWithTrigger("nope", StatusDone, ""); err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestLogRemoveTombstones(t *testing.T) {
	s := newLog(t, "")
	a, _ := s.AddWithTrigger("temp", "", "")
	b, _ := s.AddWithTrigger("keep", "", "")

	if _, err := s.Remove(a.HarpID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := s.List(nil, "")
	if len(got) != 1 || got[0].HarpID != b.HarpID {
		t.Fatalf("after remove, want only %s, got %+v", b.HarpID, got)
	}
	if _, err := s.Remove(a.HarpID); err == nil {
		t.Fatal("expected error removing an already-removed task")
	}
}

func TestLogSummarize(t *testing.T) {
	s := newLog(t, "")
	a, _ := s.AddWithTrigger("a", "", "")
	_, _ = s.AddWithTrigger("b", "", "")
	_, _ = s.SetStatusWithTrigger(a.HarpID, StatusInProgress, "")

	sum, err := s.Summarize()
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if sum.Counts[StatusToDo] != 1 || sum.Counts[StatusInProgress] != 1 {
		t.Fatalf("counts = %+v", sum.Counts)
	}
	if len(sum.InProgress) != 1 || sum.InProgress[0] != a.HarpID {
		t.Fatalf("in-progress = %+v", sum.InProgress)
	}
}

// TestLogFailsLoudOnMalformedLine pins the fail-loud contract (CLAUDE.md): a
// malformed line must fail the whole fold with an error naming the file, the
// 1-based line number, and a fix-it hint — never be silently skipped.
func TestLogFailsLoudOnMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"good one","status":"To Do","ts":"2026-01-01T00:00:00Z"}
this is not json
{"op":"add","task":"beta","text":"another","status":"To Do","ts":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err == nil {
		t.Fatalf("expected error for malformed line, got tasks: %+v", got)
	}
	if got != nil {
		t.Fatalf("expected no tasks on a fold error, got %+v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Fatalf("error %q does not name the log file path", msg)
	}
	if !strings.Contains(msg, ":2:") {
		t.Fatalf("error %q does not name the 1-based line number", msg)
	}
	if !strings.Contains(msg, "inspect/repair") && !strings.Contains(msg, "re-add") {
		t.Fatalf("error %q does not carry a fix-it hint", msg)
	}
}

// TestLogFailsLoudOnUnknownOp pins the same contract for a record this reader
// can't interpret: an unrecognized op is a fatal fold error (a log written by
// a newer taskloom), not a silent drop.
func TestLogFailsLoudOnUnknownOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"good one","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"reprioritize","task":"alpha","ts":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err == nil {
		t.Fatalf("expected error for unknown op, got tasks: %+v", got)
	}
	if got != nil {
		t.Fatalf("expected no tasks on a fold error, got %+v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Fatalf("error %q does not name the log file path", msg)
	}
	if !strings.Contains(msg, ":2:") {
		t.Fatalf("error %q does not name the 1-based line number", msg)
	}
	if !strings.Contains(msg, "reprioritize") {
		t.Fatalf("error %q does not name the unrecognized op", msg)
	}
	if !strings.Contains(msg, "upgrade") {
		t.Fatalf("error %q does not hint at upgrading the binary", msg)
	}
}

// TestLogMixedShapeConformance covers a log whose early lines are valid op
// events and whose Nth line is foreign-shaped JSON: parseable JSON with no
// "op" key, so json.Unmarshal succeeds with a zero-value Op and lands in the
// unknown-op path. The error must still name the offending line, and the
// valid preceding records must NOT be partially applied into a returned view.
func TestLogMixedShapeConformance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"first","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"beta","text":"second","status":"To Do","ts":"2026-01-01T00:00:01Z"}
{"harp_id":"x","status":"To Do"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err == nil {
		t.Fatalf("expected error for foreign-shaped record, got tasks: %+v", got)
	}
	if got != nil {
		t.Fatalf("expected no partially-applied tasks, got %+v", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, ":3:") {
		t.Fatalf("error %q does not name line 3", msg)
	}
	if !strings.Contains(msg, "no op field") {
		t.Fatalf("error %q does not clarify the record has no op field, got: %s", msg, msg)
	}
}

func TestLogMintUniqueUnderConcurrency(t *testing.T) {
	s := newLog(t, "")
	const n = 40
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			if _, err := s.AddWithTrigger("task", "", ""); err != nil {
				t.Errorf("add %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, _ := s.List(nil, "")
	if len(got) != n {
		t.Fatalf("want %d tasks, got %d", n, len(got))
	}
	seen := map[string]bool{}
	for _, task := range got {
		if seen[task.HarpID] {
			t.Fatalf("duplicate harp under concurrency: %s", task.HarpID)
		}
		seen[task.HarpID] = true
	}
}

func TestLogRepairReintroducesDisplacedAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	// Two adds claim the same harp (a concurrent-mint collision the file lock
	// would normally prevent). The first holds it; the second is displaced.
	raw := `{"op":"add","task":"alpha","text":"first writer","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"alpha","text":"displaced writer","status":"To Do","ts":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")

	// Pre-repair: this is exactly the real-collision case Part 2 targets
	// (two different tasks minted the same harp), so a read must fail loud
	// rather than silently surface only the survivor.
	if got, err := s.List(nil, ""); err == nil {
		t.Fatalf("expected a loud error for the unresolved harp collision, got tasks: %+v", got)
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "alpha") || !strings.Contains(msg, "first writer") || !strings.Contains(msg, "displaced writer") {
			t.Fatalf("collision error missing harp/texts: %q", msg)
		}
	}

	if err := s.Repair(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("post-repair list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("post-repair want 2 tasks, got %d: %+v", len(got), got)
	}
	var texts []string
	for _, task := range got {
		texts = append(texts, task.Text)
	}
	if !slices.Contains(texts, "displaced writer") {
		t.Fatalf("displaced task not re-introduced: %v", texts)
	}

	// Idempotent: a second repair must not duplicate again, and the read
	// must stay clean (the resolved anomaly must not keep failing loud).
	if err := s.Repair(); err != nil {
		t.Fatalf("repair2: %v", err)
	}
	got, err = s.List(nil, "")
	if err != nil {
		t.Fatalf("post-repair2 list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("repair not idempotent, got %d tasks", len(got))
	}
}

// TestLogRepairPreservesTagsAndCreatedAt pins the fix: repair()'s re-add
// event dropped the displaced task's Tags and Ts entirely, so the
// recovered task came back detagged and with CreatedAt silently reset to
// the repair's own timestamp — a payload-dropping "success" and, per
// priority.Compute's CreatedAt handling, a task whose age is now a lie.
func TestLogRepairPreservesTagsAndCreatedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"first writer","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"alpha","text":"displaced writer","status":"To Do","tags":["triage:kind=defect","urgent"],"ts":"2020-06-15T00:00:00Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	if err := s.Repair(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("post-repair list: %v", err)
	}
	var recovered *Task
	for i := range got {
		if got[i].Text == "displaced writer" {
			recovered = &got[i]
		}
	}
	if recovered == nil {
		t.Fatalf("displaced task not found in %+v", got)
	}
	if !slices.Contains(recovered.Tags, "triage:kind=defect") || !slices.Contains(recovered.Tags, "urgent") {
		t.Fatalf("repair must carry the displaced task's tags forward, got %v", recovered.Tags)
	}
	wantCreated := time.Date(2020, 6, 15, 0, 0, 0, 0, time.UTC)
	if !recovered.CreatedAt.Equal(wantCreated) {
		t.Fatalf("repair must carry the displaced task's original Ts forward as CreatedAt, got %v want %v", recovered.CreatedAt, wantCreated)
	}
}

// TestEventLog_WriteFailsLoudOnUnresolvedAnomaly pins the fix: every read
// in this package went dark on an unresolved harp collision (via
// snapshot()/deferredSince()'s anomalyError gate) while every write kept
// succeeding — task_add, tag, status, and text mutations landed silently
// into a store that could no longer be read at all. Writes must fail the
// same way reads do until the collision is resolved (Store.Repair).
func TestEventLog_WriteFailsLoudOnUnresolvedAnomaly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"first writer","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"alpha","text":"displaced writer","status":"To Do","ts":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")

	if _, err := s.AddWithTrigger("a new task", "", ""); err == nil {
		t.Fatal("Add must fail loud while an unresolved harp collision exists, not silently succeed into an unreadable store")
	}
	if _, err := s.SetStatusWithTrigger("alpha", StatusInProgress, ""); err == nil {
		t.Fatal("SetStatus must fail loud on an unresolved anomaly")
	}
	if _, err := s.AddTags("alpha", "urgent"); err == nil {
		t.Fatal("AddTags must fail loud on an unresolved anomaly")
	}
	if _, err := s.CurrentTags("alpha"); err == nil {
		t.Fatal("CurrentTags must fail loud on an unresolved anomaly")
	}

	// Repair resolves it; every operation must work normally afterward.
	if err := s.Repair(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, err := s.AddWithTrigger("a new task", "", ""); err != nil {
		t.Fatalf("Add after repair: %v", err)
	}
}

// TestAppendLine_LeavesFileUnchangedOnWriteFailure pins the fix: append()
// had no rollback, so a write that failed partway (ENOSPC, EIO, ...) left a
// torn line on disk — and fold() treats ANY unparseable line as fatal for
// the WHOLE log, so one failed append from this process alone would brick
// every future read and write, with "move the file aside and re-add every
// task" as the only stated recovery.
//
// A genuine mid-write OS failure (a short write followed by ENOSPC) isn't
// portably forceable from a Go unit test without OS-specific privileges
// this sandbox doesn't have (rlimits deliver SIGXFSZ asynchronously and
// proved unreliable to intercept here). Closing the file's raw fd out from
// under the still-open *os.File is a reliable, portable way to make the
// NEXT Write on it fail deterministically (EBADF) without corrupting
// anything else — it exercises the same error-handling branch appendLine
// must take on any write failure, and lets this test assert the contract
// that actually matters: the file's bytes are UNCHANGED after a failed
// append, never showing a torn line.
func TestAppendLine_LeavesFileUnchangedOnWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	const existing = "prefix-line-that-must-survive\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Close(int(f.Fd())); err != nil {
		t.Fatalf("close underlying fd: %v", err)
	}

	if err := appendLine(f, []byte("this line must never land\n")); err == nil {
		t.Fatal("expected an error writing through a closed fd")
	}

	// Re-open a fresh handle (the corrupted one is unusable) to inspect
	// what actually landed on disk.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("file changed after a failed append: got %q, want unchanged %q", got, existing)
	}
}

// TestLogRemove_HarpIsNeverReissued characterizes the half of the tombstone
// rule nothing else pins: a removed task's harp stays in the issued set, so a
// later add can never mint it again and a stale reference can never resolve to
// a different task. The fold branch that enforces this is reachable only from
// tests today (no CLI subcommand and no MCP tool expose removal), which is
// exactly why the rule needs writing down before anyone decides its fate.
func TestLogRemove_HarpIsNeverReissued(t *testing.T) {
	s := newLog(t, "")
	gone, _ := s.AddWithTrigger("temp", "", "")
	if _, err := s.Remove(gone.HarpID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// A removed harp is not merely absent from reads — it must never come back
	// as a NEW task's id.
	for range 200 {
		fresh, err := s.AddWithTrigger("another", "", "")
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		if fresh.HarpID == gone.HarpID {
			t.Fatalf("harp %s was reissued after removal", gone.HarpID)
		}
	}

	// And the tombstone survives a reload: the rule lives in the fold, not in
	// in-memory state.
	reopened, err := OpenLog(s.Path(), "swift-amber-falcon")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	all, err := reopened.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, task := range all {
		if task.HarpID == gone.HarpID {
			t.Fatalf("removed task %s came back after reload", gone.HarpID)
		}
	}
}

// TestLogFold_TruncatedFinalLineFailsLoud pins the invariant snapshot()'s
// lock rationale now states: a partially written final line (a peer process
// caught mid-append) makes the WHOLE fold fail loud, naming the file and the
// line. There is no malformed-line SKIP in this reader — the earlier wording
// described a lenient fold that fold() has never had, and the difference
// matters to the reason for taking the shared lock: the hazard a concurrent
// append creates is a hard, transient read failure for every task in the
// store, not one silently missing task.
func TestLogFold_TruncatedFinalLineFailsLoud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","task":"alpha","text":"good one","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"beta","text":"half-writ`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err == nil {
		t.Fatalf("a truncated final line must fail the fold, got tasks: %+v", got)
	}
	if got != nil {
		t.Fatalf("a failed fold must return no partial view, got %+v", got)
	}
	if !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("error %q does not name the truncated line", err.Error())
	}
}

// TestAppend_RejectsAnEventTheFoldCouldNeverRead pins the write seam's own
// admissibility rule. append() is the ONLY writer of this log, and fold()
// treats an unreadable record as fatal for the whole file, so a record that
// cannot fold is not a bad row — it is a bricked store. The seam therefore
// refuses an event with no op or no task identity, and refuses it BEFORE any
// bytes reach the file: the payload assertion below (file still absent /
// unchanged) is the real contract, since exit-status-only checking is exactly
// how a silent no-op survives here.
func TestAppend_RejectsAnEventTheFoldCouldNeverRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
	}{
		{"zero value", Event{}},
		{"no op", Event{Task: "alpha", Text: "x"}},
		{"no task identity", Event{Op: opStatus, Status: StatusToDo}},
		{"add with no identity", Event{Op: opAdd, Text: "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "taskloom.jsonl")
			l := &eventLog{path: path}
			if err := l.append(tc.ev); err == nil {
				t.Fatalf("append(%+v) succeeded; it must refuse a record fold() cannot read", tc.ev)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				b, _ := os.ReadFile(path)
				t.Fatalf("append wrote %d bytes for a rejected event: %q", len(b), string(b))
			}
		})
	}
}

// TestAppend_RejectedEventLeavesAReadableStore is the consequence half: the
// store that a rejected write was aimed at must still fold afterwards.
func TestAppend_RejectedEventLeavesAReadableStore(t *testing.T) {
	s := newLog(t, "sess")
	if _, err := s.AddWithTrigger("real work", "", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.log.append(Event{}); err == nil {
		t.Fatal("append(Event{}) succeeded")
	}
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("store unreadable after a rejected append: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want the one real task, got %+v", got)
	}
}

// TestFold_BlankIdentityAddIsCharacterizedNotRejected records what the READER
// does today with an `add` line carrying no harp and no text: it mints a task
// with a blank id and blank text rather than failing. This is characterization,
// not endorsement. The write seam now refuses to create such a line
// (see TestAppend_RejectsAnEventTheFoldCouldNeverRead), so the only source is a
// hand-edited or foreign log — and making the READER reject it is a
// persisted-format decision, not a sweep's call: it would convert a file that
// loads today into one that never loads again, which is precisely the
// permanently-unreadable-store failure this design existed to remove. If that
// decision is ever taken, this test is the one to invert.
func TestFold_BlankIdentityAddIsCharacterizedNotRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskloom.jsonl")
	raw := `{"op":"add","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"beta","text":"real work","status":"To Do","ts":"2026-01-01T00:00:01Z"}
`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(path, "")
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("the reader is deliberately lenient here; a fold error means the decision changed without inverting this test: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, task := range got {
		ids = append(ids, task.HarpID)
	}
	if !slices.Contains(ids, "") {
		t.Fatalf("blank-identity add is no longer folded into a task; ids = %v", ids)
	}
	if !slices.Contains(ids, "beta") {
		t.Fatalf("the real task must still fold; ids = %v", ids)
	}
}

// TestCloseAfter_ReportsACloseFailureOnASuccessfulWrite pins the log's only
// write path against reporting success for an event that may not be on disk.
// A close error after a SUCCEEDED write is the result; a close
// error after a failed one is noise, and the original diagnosis must survive.
func TestCloseAfter_ReportsACloseFailureOnASuccessfulWrite(t *testing.T) {
	open := func() *os.File {
		f, err := os.Create(filepath.Join(t.TempDir(), "x"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return f
	}

	f := open()
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := closeAfter(f, nil); err == nil {
		t.Fatal("closeAfter swallowed a close failure on a successful write")
	}

	f2 := open()
	if err := f2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	want := errors.New("the write itself failed")
	if got := closeAfter(f2, want); !errors.Is(got, want) {
		t.Fatalf("closeAfter = %v, want the original write error %v", got, want)
	}
}

// TestReads_FallBackToUnlockedWhenTheSharedLockCannotBeTaken pins the
// deliberate decision the three read paths document: the cross-process shared
// lock is BEST-EFFORT, and a lock failure downgrades to an unlocked read
// rather than failing, because a read must never block or die on lock
// acquisition. Turning that into a hard error is the change this
// test exists to catch: it would make every read of a store whose .lock is
// unusable fail, on a path whose whole point is that it stays available.
//
// The lock failure is produced structurally — a DIRECTORY where the lock
// wants to open a file — so no mocking is involved.
func TestReads_FallBackToUnlockedWhenTheSharedLockCannotBeTaken(t *testing.T) {
	s := newLog(t, "sess")
	added, err := s.AddWithTrigger("still readable", "", "")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.AddTags(added.HarpID, "urgent"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	blockLockPath(t, s.Path()+".lock")

	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("List must fall back to an unlocked read, got %v", err)
	}
	if len(got) != 1 || got[0].HarpID != added.HarpID {
		t.Fatalf("List = %+v, want the one task", got)
	}
	tags, err := s.CurrentTags(added.HarpID)
	if err != nil {
		t.Fatalf("CurrentTags must fall back to an unlocked read, got %v", err)
	}
	if !slices.Contains(tags, "urgent") {
		t.Fatalf("tags = %v", tags)
	}
	if _, err := s.DeferredSince(); err != nil {
		t.Fatalf("DeferredSince must fall back to an unlocked read, got %v", err)
	}
}

// TestApply_EventsForAnUnknownHarpAreSilentlyDropped measures that the
// fold's own doc says "a reader must never silently drop a record", and
// apply()'s status/text/tag/untag arms do exactly that whenever byID has no
// entry for the event's harp. All three ways that happens are recorded here,
// because they are NOT the same defect and a single "fail the fold" remedy
// would be wrong for two of them:
//
//   - after a remove: the drop is correct and must stay (tombstoned task).
//   - clock skew across machines: the log is git-merged and replayed in ts
//     order, so a status stamped BEFORE its own add is replayed before the
//     task exists and is lost. This is the real payload loss.
//   - a harp that was never issued at all: a genuinely dangling record.
//
// Escalated rather than fixed: what an event addressed to an unknown harp
// MEANS is a persisted-format decision, and the obvious remedy (fatal fold)
// would turn a merge artifact into a permanently unreadable store — the
// exact failure this design was meant to remove.
func TestApply_EventsForAnUnknownHarpAreSilentlyDropped(t *testing.T) {
	statusOf := func(t *testing.T, raw string) map[string]string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "taskloom.jsonl")
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		s, _ := OpenLog(path, "")
		got, err := s.List(nil, "")
		if err != nil {
			t.Fatalf("fold: %v", err)
		}
		out := map[string]string{}
		for _, task := range got {
			out[task.HarpID] = task.Status
		}
		return out
	}

	// A status event replayed BEFORE its own add (writer clock skew, the case
	// the chronological fold cannot order correctly) is lost without a word.
	skew := statusOf(t, `{"op":"status","task":"alpha","status":"In Progress","ts":"2026-01-01T00:00:00Z"}
{"op":"add","task":"alpha","text":"skewed","status":"To Do","ts":"2026-01-01T00:00:05Z"}
`)
	if skew["alpha"] != StatusToDo {
		t.Fatalf("status = %q; this test records the CURRENT silent-drop behaviour", skew["alpha"])
	}

	// An event for a harp that was never issued at all: dropped, no error.
	dangling := statusOf(t, `{"op":"add","task":"alpha","text":"real","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"tag","task":"ghost","tags":["urgent"],"ts":"2026-01-01T00:00:01Z"}
`)
	if len(dangling) != 1 || dangling["alpha"] != StatusToDo {
		t.Fatalf("fold = %v, want just the real task", dangling)
	}

	// After a remove, the drop is CORRECT and must survive any future fix.
	removed := statusOf(t, `{"op":"add","task":"alpha","text":"real","status":"To Do","ts":"2026-01-01T00:00:00Z"}
{"op":"remove","task":"alpha","ts":"2026-01-01T00:00:01Z"}
{"op":"status","task":"alpha","status":"Done","ts":"2026-01-01T00:00:02Z"}
`)
	if len(removed) != 0 {
		t.Fatalf("fold = %v, want an empty view: a status for a tombstoned task is correctly ignored", removed)
	}
}

// blockLockPath makes a shared lock fail to acquire on path, structurally: a
// DIRECTORY cannot be opened as a lock file. Nothing is mocked, so the
// fallback under test is the real one.
func blockLockPath(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove lock file: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("block the lock path: %v", err)
	}
	fl, err := newFileLock(path)
	if err == nil {
		err = fl.RLock()
	}
	if err == nil {
		t.Fatalf("the shared lock at %s was acquired; this test no longer exercises the fallback", path)
	}
}

// TestAppend_CreatesLogInAFreshDirectory is the characterization half of
// append()'s directory-sync-on-create step (see iox.SyncDir's call site in
// log.go): moved here from the now-collapsed syncdir_unix_test.go, whose
// own low-level primitive test (TestSyncDir) lives in package iox now,
// where the primitive itself lives. This pins that the added directory
// flush does not change what append() does on the happy path — a first
// event into a not-yet-existing nested directory still lands and still
// folds back.
func TestAppend_CreatesLogInAFreshDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "taskloom.jsonl")
	s, err := OpenLog(path, "sess")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.AddWithTrigger("first event", "", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log not written at %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "first event") {
		t.Fatalf("log does not carry the event: %q", string(raw))
	}
	got, err := s.List(nil, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, err = %v", got, err)
	}
}
