package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func addTask(t *testing.T, storePath, text, status string) {
	t.Helper()
	s := &Store{path: storePath}
	if _, err := s.Add(text, status); err != nil {
		t.Fatalf("seed Add(%q): %v", text, err)
	}
}

// writeHarpTask seeds a legacy markdown store with a task carrying an explicit
// harp id (Add would mint a random one), so migration dedup can be exercised.
func writeHarpTask(t *testing.T, storePath, harpID, text, status string) {
	t.Helper()
	if _, err := OpenPath(storePath).insert(Task{HarpID: harpID, Text: text, Status: status}); err != nil {
		t.Fatalf("insert %q: %v", harpID, err)
	}
}

func taskTexts(t *testing.T, s *Store) []string {
	t.Helper()
	list, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make([]string, len(list))
	for i, tk := range list {
		out[i] = tk.Text
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestHarpStorePath(t *testing.T) {
	got := HarpStorePath("/root/sessions", "swift-amber-falcon")
	want := filepath.Join("/root/sessions", "swift-amber-falcon", "tasks.md")
	if got != want {
		t.Fatalf("HarpStorePath = %q, want %q", got, want)
	}
}

// TestMigrateLegacyIfNeeded covers the one-time import of pre-ADR-0025 markdown
// stores into the per-project log: per-harp tasks keep their harp as origin,
// the legacy project store contributes origin-less tasks, and status survives.
func TestMigrateLegacyIfNeeded(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, HarpStorePath(root, "harp-x"), "harp task", StatusInProgress)
	addTask(t, legacyStorePath(proj), "legacy task", StatusToDo)

	logPath := filepath.Join(t.TempDir(), "tasks.jsonl")
	if err := MigrateLegacyIfNeeded(logPath, proj, root, []string{"harp-x"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	s, err := OpenLog(logPath, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.List(nil, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 migrated tasks, got %d: %+v", len(got), got)
	}
	byText := map[string]Task{}
	for _, tk := range got {
		byText[tk.Text] = tk
	}
	if byText["harp task"].OriginSession != "harp-x" {
		t.Fatalf("harp task origin = %q, want harp-x", byText["harp task"].OriginSession)
	}
	if byText["legacy task"].OriginSession != "" {
		t.Fatalf("legacy task origin = %q, want empty", byText["legacy task"].OriginSession)
	}
	if byText["harp task"].Status != StatusInProgress {
		t.Fatalf("status not preserved: %q", byText["harp task"].Status)
	}
}

func TestMigrateLegacyIfNeeded_NoOpWhenLogExists(t *testing.T) {
	proj := t.TempDir()
	addTask(t, legacyStorePath(proj), "legacy", StatusToDo)

	logPath := filepath.Join(t.TempDir(), "tasks.jsonl")
	s, err := OpenLog(logPath, "sess")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("existing", ""); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyIfNeeded(logPath, proj, "", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := s.List(nil, "")
	if len(got) != 1 || got[0].Text != "existing" {
		t.Fatalf("migration ran over an existing log: %+v", got)
	}
	if !exists(legacyStorePath(proj)) {
		t.Error("migration must not delete the legacy source")
	}
}

// TestMigrateLegacyIfNeeded_DedupMostRecentWins verifies that a harp id present
// in more than one per-harp store is imported once, with the most-recent
// session (first in the harp list) winning.
func TestMigrateLegacyIfNeeded_DedupMostRecentWins(t *testing.T) {
	root := t.TempDir()
	writeHarpTask(t, HarpStorePath(root, "recent"), "shared-harp", "newer text", StatusDone)
	writeHarpTask(t, HarpStorePath(root, "older"), "shared-harp", "older text", StatusToDo)

	logPath := filepath.Join(t.TempDir(), "tasks.jsonl")
	if err := MigrateLegacyIfNeeded(logPath, "", root, []string{"recent", "older"}); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenLog(logPath, "")
	got, _ := s.List(nil, "")
	if len(got) != 1 {
		t.Fatalf("dedup by harp id failed, got %d: %+v", len(got), got)
	}
	if got[0].Text != "newer text" || got[0].Status != StatusDone {
		t.Fatalf("most-recent should win, got %+v", got[0])
	}
}
