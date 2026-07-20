package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProjectID_AcceptsHarpNamesAndCustomIDs(t *testing.T) {
	for _, id := range []string{
		"swift-amber-falcon",
		"a",
		"Project_1",
		"v1.2.3",
		"team-alpha_2026",
	} {
		if err := ValidateProjectID(id); err != nil {
			t.Errorf("ValidateProjectID(%q) = %v, want nil", id, err)
		}
	}
}

func TestValidateProjectID_RejectsTraversalAndSeparators(t *testing.T) {
	for _, id := range []string{
		"",
		".",
		"..",
		"../../../home/user/.bashrc",
		"a/b",
		`a\b`,
		"foo/../bar",
		".hidden",
		"with space",
		"tab\tname",
		"null\x00byte",
		"..foo",
		strings.Repeat("x", 256),
	} {
		if err := ValidateProjectID(id); err == nil {
			t.Errorf("ValidateProjectID(%q) = nil, want error", id)
		}
	}
}

// TestTasksLogPath_RejectsTraversal proves the file-path chokepoint refuses a
// crafted id before it can steer a write outside ~/.ctxloom/tasks.
func TestTasksLogPath_RejectsTraversal(t *testing.T) {
	if _, err := TasksLogPath(ModeHome, "", "../../escape"); err == nil {
		t.Fatal("TasksLogPath(traversal) = nil error, want rejection")
	}
	got, err := TasksLogPath(ModeHome, "", "swift-amber-falcon")
	if err != nil {
		t.Fatalf("TasksLogPath(valid) = %v", err)
	}
	if filepath.Base(got) != "swift-amber-falcon"+TasksLogExt {
		t.Fatalf("TasksLogPath base = %q", filepath.Base(got))
	}
}

// TestTasksLogPath_EmptyModeDefaultsToHome proves "" (the zero Mode value)
// behaves exactly like ModeHome — every caller that predates homing-mode
// selection (TaskContext{}'s zero HomingMode) must keep resolving under
// ~/.ctxloom/tasks unchanged.
func TestTasksLogPath_EmptyModeDefaultsToHome(t *testing.T) {
	viaEmpty, err := TasksLogPath("", "", "swift-amber-falcon")
	if err != nil {
		t.Fatalf("TasksLogPath(\"\") = %v", err)
	}
	viaHome, err := TasksLogPath(ModeHome, "", "swift-amber-falcon")
	if err != nil {
		t.Fatalf("TasksLogPath(ModeHome) = %v", err)
	}
	if viaEmpty != viaHome {
		t.Fatalf("TasksLogPath(\"\") = %q, want it to match ModeHome's %q", viaEmpty, viaHome)
	}
}

// TestTasksLogPath_RepoMode proves ModeRepo resolves inside repoRoot at the
// documented .taskloom/tasks.jsonl location, ignoring projectID entirely —
// the repo path is the identity in this mode (see internal/taskloom/config).
func TestTasksLogPath_RepoMode(t *testing.T) {
	got, err := TasksLogPath(ModeRepo, "/some/repo", "should-be-ignored")
	if err != nil {
		t.Fatalf("TasksLogPath(ModeRepo) = %v", err)
	}
	want := filepath.Join("/some/repo", RepoDirName, RepoTasksFileName)
	if got != want {
		t.Fatalf("TasksLogPath(ModeRepo) = %q, want %q", got, want)
	}
}

// TestTasksLogPath_RepoModeRequiresRoot proves ModeRepo fails loud rather
// than silently resolving relative to "" when no repo root was resolved.
func TestTasksLogPath_RepoModeRequiresRoot(t *testing.T) {
	if _, err := TasksLogPath(ModeRepo, "", "whatever"); err == nil {
		t.Fatal("TasksLogPath(ModeRepo, \"\", ...) = nil error, want rejection")
	}
}

// TestTasksLogPath_UnknownModeIsError proves an unrecognized Mode value is a
// reported error, never a silent fallback to either convention.
func TestTasksLogPath_UnknownModeIsError(t *testing.T) {
	if _, err := TasksLogPath(Mode("bogus"), "/repo", "id"); err == nil {
		t.Fatal("TasksLogPath(bogus mode) = nil error, want rejection")
	}
}
