package tasks

import (
	"os"
	"path/filepath"
	"slices"
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

func TestLogSkipsMalformedLines(t *testing.T) {
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
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tasks (malformed line skipped), got %d: %+v", len(got), got)
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

	got, _ := s.List(nil, "")
	if len(got) != 1 || got[0].Text != "first writer" {
		t.Fatalf("pre-repair want only first writer, got %+v", got)
	}

	if err := s.Repair(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	got, _ = s.List(nil, "")
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

	// Idempotent: a second repair must not duplicate again.
	if err := s.Repair(); err != nil {
		t.Fatalf("repair2: %v", err)
	}
	got, _ = s.List(nil, "")
	if len(got) != 2 {
		t.Fatalf("repair not idempotent, got %d tasks", len(got))
	}
}
