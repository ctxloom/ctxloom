package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, dir
}

func TestOpen_RequiresProjectDir(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("Open(\"\") should error")
	}
}

func TestSnapshot_MissingFile(t *testing.T) {
	s, _ := newTestStore(t)
	tasks, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot on missing file: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("missing file should yield 0 tasks, got %d", len(tasks))
	}
}

func TestAdd_PersistsAndAssignsHarpID(t *testing.T) {
	s, dir := newTestStore(t)
	task, err := s.Add("write the storage layer", "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if task.HarpID == "" {
		t.Error("Add should assign a harp ID")
	}
	if task.Status != StatusToDo {
		t.Errorf("empty status should default to %q, got %q", StatusToDo, task.Status)
	}

	// File should exist now under .ctxloom/tasks.md.
	if _, err := os.Stat(filepath.Join(dir, ".ctxloom", "tasks.md")); err != nil {
		t.Errorf("tasks.md should exist after Add: %v", err)
	}

	all, err := s.Snapshot()
	if err != nil || len(all) != 1 || all[0].HarpID != task.HarpID {
		t.Errorf("Snapshot should return the one task we added; got %+v err=%v", all, err)
	}
}

func TestAdd_EmptyTextRejected(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Add("   ", ""); err == nil {
		t.Error("blank text should error")
	}
}

// TestAdd_ConcurrentNoLostWrites hammers Add from many goroutines sharing one
// Store. The mutex + file lock must serialize the read-modify-write so every
// task lands with a unique harp ID and none are lost to a clobbered rewrite.
// Run with -race to catch unsynchronized access to the backing file.
func TestAdd_ConcurrentNoLostWrites(t *testing.T) {
	s, _ := newTestStore(t)
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			if _, err := s.Add("task "+string(rune('a'+i%26)), ""); err != nil {
				t.Errorf("concurrent Add: %v", err)
			}
		}(i)
	}
	wg.Wait()

	all, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(all) != n {
		t.Fatalf("expected %d tasks after concurrent Add, got %d", n, len(all))
	}
	seen := make(map[string]bool, n)
	for _, task := range all {
		if seen[task.HarpID] {
			t.Errorf("duplicate harp ID survived concurrent Add: %s", task.HarpID)
		}
		seen[task.HarpID] = true
	}
}

func TestList_FiltersByStatusAndTerm(t *testing.T) {
	s, _ := newTestStore(t)
	tA, _ := s.Add("alpha task", StatusInProgress)
	tB, _ := s.Add("beta task", StatusToDo)
	tC, _ := s.Add("gamma task", StatusDone)

	// Status filter
	got, err := s.List([]string{StatusInProgress, StatusToDo}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("status filter: want 2, got %d", len(got))
	}

	// Term filter
	got, _ = s.List(nil, "BETA")
	if len(got) != 1 || got[0].HarpID != tB.HarpID {
		t.Errorf("term filter: want [%s], got %+v", tB.HarpID, got)
	}

	// Combined
	got, _ = s.List([]string{StatusDone}, "gamma")
	if len(got) != 1 || got[0].HarpID != tC.HarpID {
		t.Errorf("combined filter: want [%s], got %+v", tC.HarpID, got)
	}

	// No matches
	if got, _ := s.List([]string{StatusDone}, "alpha"); len(got) != 0 {
		t.Errorf("no-match filter: want 0, got %d", len(got))
	}

	_ = tA
}

func TestSetStatus_MovesTask(t *testing.T) {
	s, _ := newTestStore(t)
	task, _ := s.Add("do the thing", StatusToDo)

	updated, err := s.SetStatus(task.HarpID, StatusInProgress)
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if updated.Status != StatusInProgress {
		t.Errorf("status: want %q, got %q", StatusInProgress, updated.Status)
	}
	if updated.Checked {
		t.Error("InProgress should not be checked")
	}

	updated, _ = s.SetStatus(task.HarpID, StatusDone)
	if !updated.Checked {
		t.Error("Done should be checked")
	}
}

func TestSetStatus_UnknownHarpID(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.SetStatus("no-such-id", StatusDone); err == nil {
		t.Error("unknown harp ID should error")
	}
}

func TestSummarize(t *testing.T) {
	s, _ := newTestStore(t)
	tA, _ := s.Add("a", StatusInProgress)
	_, _ = s.Add("b", StatusInProgress)
	_, _ = s.Add("c", StatusDone)

	sum, err := s.Summarize()
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if sum.Counts[StatusInProgress] != 2 {
		t.Errorf("InProgress count: want 2, got %d", sum.Counts[StatusInProgress])
	}
	if sum.Counts[StatusDone] != 1 {
		t.Errorf("Done count: want 1, got %d", sum.Counts[StatusDone])
	}
	if len(sum.InProgress) != 2 {
		t.Errorf("InProgress IDs: want 2, got %d", len(sum.InProgress))
	}
	// One of them is tA; we don't care about order, just presence.
	found := false
	for _, id := range sum.InProgress {
		if id == tA.HarpID {
			found = true
		}
	}
	if !found {
		t.Errorf("InProgress should include %s, got %v", tA.HarpID, sum.InProgress)
	}
}

func TestParseAndRender_RoundTrip(t *testing.T) {
	// Hand-author a file, parse it, render it back, parse again — both
	// task sets should match (ignoring TextHash since it's recomputed).
	src := `# Tasks

<!-- ctxloom-tasks:v1 -->

## In Progress

- [ ] ` + "`swift-amber-falcon`" + ` implement TodoWrite hook

## To Do

- [ ] ` + "`quiet-silver-meadow`" + ` write storage layer

## Done

- [x] ` + "`misty-golden-river`" + ` spike session naming
`
	parsed := parseFile(src)
	if len(parsed) != 3 {
		t.Fatalf("parse: want 3 tasks, got %d", len(parsed))
	}

	rendered := renderFile(parsed)
	reparsed := parseFile(rendered)
	if len(reparsed) != 3 {
		t.Fatalf("re-parse: want 3 tasks, got %d", len(reparsed))
	}
	for i := range parsed {
		if parsed[i].HarpID != reparsed[i].HarpID ||
			parsed[i].Text != reparsed[i].Text ||
			parsed[i].Status != reparsed[i].Status ||
			parsed[i].Checked != reparsed[i].Checked {
			t.Errorf("round-trip mismatch at %d:\n  before: %+v\n  after:  %+v", i, parsed[i], reparsed[i])
		}
	}

	// File header preserved.
	if !strings.HasPrefix(rendered, fileHeader) {
		t.Error("rendered file should start with the canonical header")
	}
}

func TestRender_PreservesSectionOrder(t *testing.T) {
	// Custom status sections appear after the defaults in first-seen order.
	tasks := []Task{
		{HarpID: "id-one", Text: "a", Status: "Custom"},
		{HarpID: "id-two", Text: "b", Status: StatusToDo},
		{HarpID: "id-three", Text: "c", Status: "Custom"},
	}
	rendered := renderFile(tasks)
	if idx1, idx2 := strings.Index(rendered, "## To Do"), strings.Index(rendered, "## Custom"); idx1 < 0 || idx2 < 0 || idx1 > idx2 {
		t.Errorf("default statuses should come before custom; To Do at %d, Custom at %d", idx1, idx2)
	}
}
