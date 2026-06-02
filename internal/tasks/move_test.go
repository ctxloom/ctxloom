package tasks

import (
	"path/filepath"
	"sort"
	"testing"
)

// seedReturning adds a task and returns its generated harp id.
func seedReturning(t *testing.T, storePath, text, status string) string {
	t.Helper()
	s := &Store{path: storePath}
	task, err := s.Add(text, status)
	if err != nil {
		t.Fatalf("seed Add(%q): %v", text, err)
	}
	return task.HarpID
}

func TestMoveTask_MovesAndSetsStatus(t *testing.T) {
	root := t.TempDir()
	srcPath := HarpStorePath(root, "harp-x")
	dstPath := HarpStorePath(root, "harp-y")
	id := seedReturning(t, srcPath, "do the thing", StatusToDo)

	src := OpenPath(srcPath)
	dst := OpenPath(dstPath)
	moved, err := MoveTask(src, dst, id, StatusInProgress)
	if err != nil {
		t.Fatalf("MoveTask: %v", err)
	}

	// Identity and text preserved; status set on arrival.
	if moved.HarpID != id {
		t.Errorf("moved harp id = %q, want %q (identity must survive the move)", moved.HarpID, id)
	}
	if moved.Status != StatusInProgress {
		t.Errorf("moved status = %q, want %q", moved.Status, StatusInProgress)
	}

	dstTasks, _ := dst.Snapshot()
	if len(dstTasks) != 1 || dstTasks[0].HarpID != id || dstTasks[0].Text != "do the thing" {
		t.Fatalf("dst tasks = %+v, want single carried task", dstTasks)
	}
	if dstTasks[0].Status != StatusInProgress {
		t.Errorf("dst task status = %q, want %q", dstTasks[0].Status, StatusInProgress)
	}

	// Source no longer holds it.
	if srcTasks, _ := src.Snapshot(); len(srcTasks) != 0 {
		t.Errorf("src tasks = %+v, want empty after move", srcTasks)
	}
}

func TestMoveTask_NotFound(t *testing.T) {
	root := t.TempDir()
	src := OpenPath(HarpStorePath(root, "harp-x"))
	dst := OpenPath(HarpStorePath(root, "harp-y"))
	if _, err := MoveTask(src, dst, "nonexistent", StatusInProgress); err == nil {
		t.Fatal("MoveTask of a missing task must error")
	}
}

func TestMoveTask_LegacySource(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	id := seedReturning(t, legacyStorePath(proj), "from legacy", StatusToDo)

	src, err := Open(proj)
	if err != nil {
		t.Fatal(err)
	}
	dst := OpenPath(HarpStorePath(root, "harp-new"))
	if _, err := MoveTask(src, dst, id, StatusInProgress); err != nil {
		t.Fatalf("MoveTask from legacy: %v", err)
	}
	if got := taskTexts(t, dst); len(got) != 1 || got[0] != "from legacy" {
		t.Fatalf("dst tasks = %v, want [from legacy]", got)
	}
	if got := taskTexts(t, src); len(got) != 0 {
		t.Errorf("legacy src should be empty after move, got %v", got)
	}
}

func TestMoveTask_NilStores(t *testing.T) {
	dst := OpenPath(filepath.Join(t.TempDir(), "tasks.md"))
	if _, err := MoveTask(nil, dst, "x", StatusInProgress); err == nil {
		t.Error("nil src must error")
	}
	if _, err := MoveTask(dst, nil, "x", StatusInProgress); err == nil {
		t.Error("nil dst must error")
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.md")
	keep := seedReturning(t, path, "keep me", StatusToDo)
	drop := seedReturning(t, path, "drop me", StatusToDo)

	s := OpenPath(path)
	removed, err := s.Remove(drop)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if removed.HarpID != drop || removed.Text != "drop me" {
		t.Errorf("removed = %+v, want the dropped task", removed)
	}
	got := taskTexts(t, s)
	if len(got) != 1 || got[0] != "keep me" {
		t.Fatalf("remaining tasks = %v, want [keep me]", got)
	}
	if _, err := s.Remove(keep); err != nil {
		t.Fatalf("Remove keep: %v", err)
	}
}

func TestRemove_NotFound(t *testing.T) {
	s := OpenPath(filepath.Join(t.TempDir(), "tasks.md"))
	if _, err := s.Remove("nope"); err == nil {
		t.Fatal("Remove of a missing harp id must error")
	}
}

func TestInsert_PreservesHarpIDAndReplaces(t *testing.T) {
	s := OpenPath(filepath.Join(t.TempDir(), "tasks.md"))
	if _, err := s.insert(Task{HarpID: "fixed-id", Text: "first", Status: StatusToDo}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Re-inserting the same id replaces in place (no duplicate row).
	if _, err := s.insert(Task{HarpID: "fixed-id", Text: "second", Status: StatusInProgress}); err != nil {
		t.Fatalf("insert replace: %v", err)
	}
	all, _ := s.Snapshot()
	if len(all) != 1 {
		t.Fatalf("want 1 row after replace, got %d: %+v", len(all), all)
	}
	if all[0].HarpID != "fixed-id" || all[0].Text != "second" || all[0].Status != StatusInProgress {
		t.Errorf("row = %+v, want replaced (fixed-id, second, In Progress)", all[0])
	}
}

func TestInsert_RequiresHarpIDAndText(t *testing.T) {
	s := OpenPath(filepath.Join(t.TempDir(), "tasks.md"))
	if _, err := s.insert(Task{Text: "no id"}); err == nil {
		t.Error("insert without harp id must error")
	}
	if _, err := s.insert(Task{HarpID: "id", Text: "   "}); err == nil {
		t.Error("insert with empty text must error")
	}
}

func TestCollectForSessions_MultiStoreTagsOrigin(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, HarpStorePath(root, "harp-a"), "a-todo", StatusToDo)
	addTask(t, HarpStorePath(root, "harp-a"), "a-done", StatusDone)
	addTask(t, HarpStorePath(root, "harp-b"), "b-inprog", StatusInProgress)
	addTask(t, legacyStorePath(proj), "legacy-todo", StatusToDo)

	located, err := CollectForSessions(root, proj, []string{"harp-a", "harp-b"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	byText := map[string]Located{}
	for _, l := range located {
		byText[l.Text] = l
	}
	cases := map[string]string{ // text -> expected origin
		"a-todo":      "harp-a",
		"a-done":      "harp-a",
		"b-inprog":    "harp-b",
		"legacy-todo": "", // legacy store
	}
	if len(located) != len(cases) {
		t.Fatalf("collected %d tasks, want %d: %+v", len(located), len(cases), located)
	}
	for text, wantOrigin := range cases {
		l, ok := byText[text]
		if !ok {
			t.Errorf("missing task %q", text)
			continue
		}
		if l.OriginHarp != wantOrigin {
			t.Errorf("task %q origin = %q, want %q", text, l.OriginHarp, wantOrigin)
		}
	}
}

func TestCollectForSessions_StatusFilter(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, HarpStorePath(root, "harp-a"), "open one", StatusToDo)
	addTask(t, HarpStorePath(root, "harp-a"), "closed one", StatusDone)

	located, err := CollectForSessions(root, proj, []string{"harp-a"}, []string{StatusToDo, StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(located))
	for i, l := range located {
		got[i] = l.Text
	}
	sort.Strings(got)
	if len(got) != 1 || got[0] != "open one" {
		t.Fatalf("filtered tasks = %v, want [open one]", got)
	}
}

func TestCollectForSessions_MissingStoresContributeNothing(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	// No stores written at all.
	located, err := CollectForSessions(root, proj, []string{"ghost-1", "ghost-2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(located) != 0 {
		t.Fatalf("want no tasks from missing stores, got %+v", located)
	}
}
