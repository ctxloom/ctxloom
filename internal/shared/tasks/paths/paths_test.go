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
	if _, err := TasksLogPath("../../escape"); err == nil {
		t.Fatal("TasksLogPath(traversal) = nil error, want rejection")
	}
	got, err := TasksLogPath("swift-amber-falcon")
	if err != nil {
		t.Fatalf("TasksLogPath(valid) = %v", err)
	}
	if filepath.Base(got) != "swift-amber-falcon"+TasksLogExt {
		t.Fatalf("TasksLogPath base = %q", filepath.Base(got))
	}
}
