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

// TestValidateProjectID_MessagePerArm characterizes which arm rejects which
// input, by the message each arm emits. ValidateProjectID's arms are not
// independent — the rune scan already rejects every separator, so the
// "" / ".." / leading-dot guards after it are the only ones a clean-charset
// id can reach — and a refactor that collapses them must keep each input
// landing on the same arm it lands on today.
func TestValidateProjectID_MessagePerArm(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"", "is empty"},
		{strings.Repeat("a", 256), "is too long"},
		{"has/slash", "invalid character"},
		{"has space", "invalid character"},
		{"has\x00nul", "invalid character"},
		{"tab\there", "invalid character"},
		{"a..b", "not a valid path segment"},
		{"..", "not a valid path segment"},
		{"..foo", "not a valid path segment"},
		{".", "not a valid path segment"},
		{".hidden", "must not start with a dot"},
	}
	for _, tc := range cases {
		err := ValidateProjectID(tc.id)
		if err == nil {
			t.Errorf("ValidateProjectID(%q) = nil, want an error mentioning %q", tc.id, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidateProjectID(%q) = %v, want a message mentioning %q", tc.id, err, tc.want)
		}
	}

	accepted := []string{
		"swift-amber-falcon", "a", strings.Repeat("a", 255),
		"under_score", "dots.in.middle", "Mixed-CASE-09",
	}
	for _, id := range accepted {
		if err := ValidateProjectID(id); err != nil {
			t.Errorf("ValidateProjectID(%q) = %v, want nil", id, err)
		}
	}
}
