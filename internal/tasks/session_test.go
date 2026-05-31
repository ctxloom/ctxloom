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

func TestOpenSession_NoHarp_FallsBackToLegacy(t *testing.T) {
	proj := t.TempDir()
	s, err := OpenSession(SessionConfig{ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if s.Path() != legacyStorePath(proj) {
		t.Fatalf("path = %q, want legacy %q", s.Path(), legacyStorePath(proj))
	}
}

func TestOpenSession_EmptySessionsRoot_FallsBackToLegacy(t *testing.T) {
	proj := t.TempDir()
	s, err := OpenSession(SessionConfig{Harp: "h", ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if s.Path() != legacyStorePath(proj) {
		t.Fatalf("path = %q, want legacy fallback %q", s.Path(), legacyStorePath(proj))
	}
}

func TestOpenSession_Fresh_MigratesLegacy(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, legacyStorePath(proj), "legacy task", StatusToDo)

	s, err := OpenSession(SessionConfig{Harp: "harp-y", SessionsRoot: root, ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if s.Path() != HarpStorePath(root, "harp-y") {
		t.Fatalf("path = %q, want harp store", s.Path())
	}
	if got := taskTexts(t, s); len(got) != 1 || got[0] != "legacy task" {
		t.Fatalf("harp store tasks = %v, want [legacy task]", got)
	}
	if exists(legacyStorePath(proj)) {
		t.Error("legacy store should have been moved away (atomic rename), but still exists")
	}
}

func TestOpenSession_Fresh_NoLegacy_StartsEmpty(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()

	s, err := OpenSession(SessionConfig{Harp: "h", SessionsRoot: root, ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskTexts(t, s); len(got) != 0 {
		t.Fatalf("want empty store, got %v", got)
	}
	if exists(HarpStorePath(root, "h")) {
		t.Error("harp store file should be created lazily on first Add, not on open")
	}
}

func TestOpenSession_ResumeWithTasks_MigratesFromPriorHarp(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, HarpStorePath(root, "harp-x"), "carried task", StatusInProgress)

	s, err := OpenSession(SessionConfig{
		Harp: "harp-y", ResumedFrom: "harp-x", RestoreTasks: true,
		SessionsRoot: root, ProjectDir: proj,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskTexts(t, s); len(got) != 1 || got[0] != "carried task" {
		t.Fatalf("resumed harp tasks = %v, want [carried task]", got)
	}
	if exists(HarpStorePath(root, "harp-x")) {
		t.Error("prior harp store should have been moved into the new harp")
	}
}

func TestOpenSession_ResumeWithoutTasks_StartsEmptyAndKeepsPrior(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, HarpStorePath(root, "harp-x"), "carried task", StatusToDo)

	s, err := OpenSession(SessionConfig{
		Harp: "harp-y", ResumedFrom: "harp-x", RestoreTasks: false,
		SessionsRoot: root, ProjectDir: proj,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskTexts(t, s); len(got) != 0 {
		t.Fatalf("declined task carry-over should yield empty store, got %v", got)
	}
	if !exists(HarpStorePath(root, "harp-x")) {
		t.Error("prior harp store must be left untouched when tasks are not restored")
	}
}

func TestOpenSession_Idempotent_DoesNotRePull(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, legacyStorePath(proj), "first", StatusToDo)

	if _, err := OpenSession(SessionConfig{Harp: "h", SessionsRoot: root, ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}
	// Legacy was migrated away; simulate a new project-local file appearing.
	addTask(t, legacyStorePath(proj), "second", StatusToDo)

	s2, err := OpenSession(SessionConfig{Harp: "h", SessionsRoot: root, ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskTexts(t, s2); len(got) != 1 || got[0] != "first" {
		t.Fatalf("established harp store should keep only the migrated 'first', got %v", got)
	}
	if !exists(legacyStorePath(proj)) {
		t.Error("a new legacy file must NOT be consumed once the harp store exists (no re-pull)")
	}
}

// TestOpenSession_RenameRaceLoserGetsEmpty models the concurrency guarantee:
// once one fresh session has consumed the legacy file, a different fresh
// session (distinct harp) finds the source gone and is left with an empty
// store rather than clobbering anything.
func TestOpenSession_RenameRaceLoserGetsEmpty(t *testing.T) {
	proj := t.TempDir()
	root := t.TempDir()
	addTask(t, legacyStorePath(proj), "legacy", StatusToDo)

	if _, err := OpenSession(SessionConfig{Harp: "a", SessionsRoot: root, ProjectDir: proj}); err != nil {
		t.Fatal(err)
	}
	sB, err := OpenSession(SessionConfig{Harp: "b", SessionsRoot: root, ProjectDir: proj})
	if err != nil {
		t.Fatal(err)
	}
	if got := taskTexts(t, sB); len(got) != 0 {
		t.Fatalf("second fresh harp must start empty after legacy consumed, got %v", got)
	}
}

func TestMoveStore_SourceMissing_NoOp(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "sub", "tasks.md")
	if err := moveStore(filepath.Join(dir, "nope.md"), dst); err != nil {
		t.Fatalf("moveStore with missing source must be a no-op, got %v", err)
	}
	if exists(dst) {
		t.Error("dst must not be created when source is missing")
	}
}
