package tasks

import (
	"path/filepath"
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
