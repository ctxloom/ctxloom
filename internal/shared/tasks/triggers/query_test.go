package triggers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryTypeValid(t *testing.T) {
	cases := []struct {
		name string
		typ  QueryType
		want bool
	}{
		{"path_exists", QueryPathExists, true},
		{"grep", QueryGrep, true},
		{"git_log_path", QueryGitLogPath, true},
		{"task_status", QueryTaskStatus, true},
		{"empty", QueryType(""), false},
		{"unknown", QueryType("shell_exec"), false},
		{"wrong-case", QueryType("Path_Exists"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.typ.Valid())
		})
	}
}

func TestQueryTypesListsAllFour(t *testing.T) {
	assert.ElementsMatch(t, []QueryType{QueryPathExists, QueryGrep, QueryGitLogPath, QueryTaskStatus}, QueryTypes())
}

func TestQueryValidate_Accepts(t *testing.T) {
	cases := []Query{
		{Type: QueryPathExists, Path: "internal/foo/bar.go"},
		{Type: QueryGrep, Pattern: "func Foo"},
		{Type: QueryGrep, Pattern: "func Foo", PathGlob: "internal/foo"},
		{Type: QueryGitLogPath, Path: "internal/foo"},
		{Type: QueryTaskStatus, HarpID: "swift-amber-falcon"},
	}
	for _, q := range cases {
		assert.NoError(t, q.Validate(), "%+v", q)
	}
}

// Adversarial: a model response is untrusted input. Every shape below must be
// rejected by Validate rather than reaching a filesystem or shell call.
func TestQueryValidate_RejectsUnwhitelistedOrEscaping(t *testing.T) {
	cases := []struct {
		name string
		q    Query
	}{
		{"unknown type", Query{Type: "shell_exec", Path: "x"}},
		{"empty type", Query{Path: "x"}},
		{"absolute path", Query{Type: QueryPathExists, Path: "/etc/passwd"}},
		{"parent traversal", Query{Type: QueryPathExists, Path: "../../etc/passwd"}},
		{"embedded traversal", Query{Type: QueryPathExists, Path: "internal/../../etc/passwd"}},
		{"empty path", Query{Type: QueryPathExists, Path: ""}},
		{"whitespace-only path", Query{Type: QueryPathExists, Path: "   "}},
		{"windows-style absolute", Query{Type: QueryPathExists, Path: `C:\Windows\System32`}},
		{"backslash traversal", Query{Type: QueryPathExists, Path: `..\..\secrets`}},
		{"null byte", Query{Type: QueryPathExists, Path: "internal/foo\x00.go"}},
		{"grep with no pattern", Query{Type: QueryGrep, Pattern: ""}},
		{"grep with escaping glob", Query{Type: QueryGrep, Pattern: "x", PathGlob: "../../etc"}},
		{"git_log_path absolute", Query{Type: QueryGitLogPath, Path: "/etc/passwd"}},
		{"git_log_path traversal", Query{Type: QueryGitLogPath, Path: "../outside"}},
		{"task_status no harp id", Query{Type: QueryTaskStatus, HarpID: ""}},
		{"task_status whitespace harp id", Query{Type: QueryTaskStatus, HarpID: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.q.Validate()
			assert.Error(t, err, "%+v should have been rejected", tc.q)
		})
	}
}

func TestSanitizeQueries_DropsInvalidKeepsValid(t *testing.T) {
	qs := []Query{
		{Type: QueryPathExists, Path: "internal/foo"},
		{Type: "shell_exec", Path: "internal/foo"},
		{Type: QueryPathExists, Path: "/etc/passwd"},
		{Type: QueryGrep, Pattern: "TODO"},
	}
	out := SanitizeQueries(qs, 0)
	require.Len(t, out, 2)
	assert.Equal(t, QueryPathExists, out[0].Type)
	assert.Equal(t, QueryGrep, out[1].Type)
}

// CHARACTERIZATION (U128-F12): a model that asked for nothing and a model
// whose every request was refused reach the caller as the SAME value. They mean
// opposite things — the first is a model declining to escalate, the second is
// ctxloom rejecting everything it asked for, which is a diagnosable fault — and
// the sanitizer's return type cannot tell them apart. Pinned here so the fix
// inverts it visibly.
func TestSanitizeQueries_AllRejectedIsIndistinguishableFromNothingAsked(t *testing.T) {
	allRejected := SanitizeQueries([]Query{
		{Type: "shell_exec", Path: "internal/foo"},
		{Type: QueryPathExists, Path: "/etc/passwd"},
		{Type: QueryGrep},
	}, 0)
	nothingAsked := SanitizeQueries(nil, 0)
	assert.Equal(t, nothingAsked, allRejected, "the two cases are currently the same value")
}

func TestSanitizeQueries_CapsPerTask(t *testing.T) {
	var qs []Query
	for i := 0; i < 10; i++ {
		qs = append(qs, Query{Type: QueryPathExists, Path: "internal/foo"})
	}
	out := SanitizeQueries(qs, 3)
	assert.Len(t, out, 3)
}

func TestSanitizeQueries_EmptyAndNilNeverPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		assert.Empty(t, SanitizeQueries(nil, 5))
		assert.Empty(t, SanitizeQueries([]Query{}, 0))
	})
}

// validateRepoPath is the syntactic containment gate: every model-authored path
// that later reaches the filesystem or a git invocation passes through here
// first, so a bypass here is a repo escape. It is exercised directly, and
// adversarially, rather than only incidentally through Validate.
//
// Containment is checked by SYNTAX only. A symlink that lives inside the repo
// but resolves outside it cannot be caught by string inspection, and no case
// here pretends otherwise — the executor re-checks containment after resolving
// symlinks, before any read (see validateRepoPath's doc comment).
func TestValidateRepoPath_RejectsEveryEscapeShape(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"unix absolute", "/etc/passwd"},
		{"parent traversal", "../../etc/passwd"},
		{"bare parent", ".."},
		{"traversal that escapes after cleaning", "internal/../../etc/passwd"},
		{"backslash traversal", `..\..\secrets`},
		{"windows drive, backslashes", `C:\Windows\System32`},

		// The backslash rule catches the usual Windows shape. The three below
		// contain no backslash at all and sail straight through it — they are
		// stopped only by the drive-letter rule, which until now nothing
		// exercised. "C:" is the length boundary of that rule.
		{"windows drive, forward slashes", "C:/Windows/System32"},
		{"windows drive, lowercase", "c:/windows/system32"},
		{"bare drive letter", "C:"},
		{"drive-relative path", "C:foo"},

		{"NUL byte", "internal/foo\x00.go"},
		{"empty", ""},
		{"whitespace only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, validateRepoPath(tc.path), "%q must be rejected", tc.path)
		})
	}
}

// The containment gate must not over-reject: a legitimate repo-relative path is
// the common case, and rejecting it silently drops the model's evidence request.
// The short paths matter — a bounds check that reads p[1] without first proving
// the path is long enough panics on a one-character path.
func TestValidateRepoPath_AcceptsContainedPaths(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"nested file", "internal/foo/bar.go"},
		{"single character", "a"},
		{"two characters, no drive colon", "ab"},
		{"dot-relative", "./internal/foo"},
		{"traversal that stays inside the repo", "a/../b"},
		{"colon that is not a drive letter", "internal/foo:bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NoError(t, validateRepoPath(tc.path), "%q must be accepted", tc.path)
		})
	}
}

// A garbled model response — huge pattern strings, deeply nested traversal,
// mixed valid/invalid queries — must never panic the sanitizer.
func TestSanitizeQueries_GarbledInputNeverPanics(t *testing.T) {
	huge := strings.Repeat("a/", 5000) + "etc/passwd"
	qs := []Query{
		{Type: QueryPathExists, Path: huge},
		{Type: QueryGrep, Pattern: strings.Repeat("x", 100000)},
		{},
		{Type: QueryPathExists},
		{Type: QueryTaskStatus},
	}
	assert.NotPanics(t, func() {
		SanitizeQueries(qs, 5)
	})
}
