package tasks

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
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

	a, err := s.Add("write the storage layer", "")
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
	a, _ := s.Add("ship it", "")

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
	a, _ := s.Add("ship it", "")
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
// a reserved operator word ("and") — both of which pkg/tagquery.ValidateTag
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
	a, _ := s.Add("ship it", "")

	if _, err := s.SetStatus(a.HarpID, StatusInProgress); err != nil {
		t.Fatalf("status1: %v", err)
	}
	if _, err := s.SetStatus(a.HarpID, StatusDone); err != nil {
		t.Fatalf("status2: %v", err)
	}

	got, _ := s.List(nil, "")
	if len(got) != 1 || got[0].Status != StatusDone || !got[0].Checked {
		t.Fatalf("want Done+checked, got %+v", got)
	}
}

func TestLogSetStatusUnknownErrors(t *testing.T) {
	s := newLog(t, "")
	if _, err := s.SetStatus("nope", StatusDone); err == nil {
		t.Fatal("expected error for unknown task")
	}
}

func TestLogRemoveTombstones(t *testing.T) {
	s := newLog(t, "")
	a, _ := s.Add("temp", "")
	b, _ := s.Add("keep", "")

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
	a, _ := s.Add("a", "")
	_, _ = s.Add("b", "")
	_, _ = s.SetStatus(a.HarpID, StatusInProgress)

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
			if _, err := s.Add("task", ""); err != nil {
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
	// Two adds claim the same harp (a concurrent-mint collision the filelock
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
