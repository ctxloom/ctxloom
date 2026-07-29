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

// TestHomeTasksLogPath_RejectsTraversal proves the file-path chokepoint
// refuses a crafted id before it can steer a write outside ~/.ctxloom/tasks.
func TestHomeTasksLogPath_RejectsTraversal(t *testing.T) {
	if _, err := HomeTasksLogPath("../../escape"); err == nil {
		t.Fatal("HomeTasksLogPath(traversal) = nil error, want rejection")
	}
	if _, err := HomeTasksLogPath(""); err == nil {
		t.Fatal("HomeTasksLogPath(\"\") = nil error, want rejection")
	}
	got, err := HomeTasksLogPath("swift-amber-falcon")
	if err != nil {
		t.Fatalf("HomeTasksLogPath(valid) = %v", err)
	}
	if filepath.Base(got) != "swift-amber-falcon"+TasksLogExt {
		t.Fatalf("HomeTasksLogPath base = %q", filepath.Base(got))
	}
}

// TestRepoTasksLogPath proves the repo-homed log resolves inside repoRoot at
// the documented .taskloom/tasks.jsonl location — the repo path is the
// identity in this mode (see internal/taskloom/config).
func TestRepoTasksLogPath(t *testing.T) {
	got, err := RepoTasksLogPath("/some/repo")
	if err != nil {
		t.Fatalf("RepoTasksLogPath = %v", err)
	}
	want := filepath.Join("/some/repo", RepoDirName, RepoTasksFileName)
	if got != want {
		t.Fatalf("RepoTasksLogPath = %q, want %q", got, want)
	}
}

// TestRepoTasksLogPath_RequiresRoot proves it fails loud rather than silently
// resolving relative to "" when no repo root was resolved.
func TestRepoTasksLogPath_RequiresRoot(t *testing.T) {
	if _, err := RepoTasksLogPath(""); err == nil {
		t.Fatal("RepoTasksLogPath(\"\") = nil error, want rejection")
	}
}
