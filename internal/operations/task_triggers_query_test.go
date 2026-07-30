package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
)

func writeRepoFile(t *testing.T, repoDir, rel, content string) {
	t.Helper()
	full := filepath.Join(repoDir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

func TestExecuteQuery_PathExists(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/foo/bar.go", "package foo\n")

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryPathExists, Path: "internal/foo/bar.go"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "exists")

	got = executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryPathExists, Path: "internal/foo/nope.go"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "does not exist")
}

// A path that syntactically passed Validate but resolves outside the repo via
// a symlink must still be rejected — the syntactic check alone (no "..",
// not absolute) cannot see this; only resolving the actual filesystem entry
// can.
func TestExecuteQuery_PathExists_RejectsSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("shh"), 0o644))

	linkPath := filepath.Join(repo, "escape-link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryPathExists, Path: "escape-link"})
	assert.NotEmpty(t, got.Err, "a symlink escaping the repo must be rejected, not followed")
}

// U088-F23: safeRepoPath treated EVERY EvalSymlinks error as "doesn't exist
// yet" and returned the unresolved path unresolved-but-unrejected — including
// a genuine resolution failure like a symlink loop (ELOOP), where the
// containment question was never actually answered. A symlink loop is a
// reliable way to trigger a non-ENOENT EvalSymlinks error without relying on
// filesystem permissions (which root bypasses).
func TestSafeRepoPath_SymlinkLoopIsRejectedNotTreatedAsMissing(t *testing.T) {
	repo := t.TempDir()
	a := filepath.Join(repo, "loop-a")
	b := filepath.Join(repo, "loop-b")
	require.NoError(t, os.Symlink(b, a))
	if err := os.Symlink(a, b); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	_, err := safeRepoPath(repo, "loop-a")
	assert.Error(t, err, "a symlink loop is a genuine resolution failure, not \"doesn't exist yet\" — it must not be waved through as an unresolved path")
}

func TestExecuteQuery_PathExists_NoRepoDirDegradesToError(t *testing.T) {
	got := executeQuery(context.Background(), &git.Fake{}, "", nil, triggers.Query{Type: triggers.QueryPathExists, Path: "x"})
	assert.NotEmpty(t, got.Err)
}

func TestExecuteQuery_Grep_FindsMatch(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/foo/bar.go", "package foo\n\nfunc Bar() {}\n")
	writeRepoFile(t, repo, "internal/foo/baz.go", "package foo\n\nfunc Baz() {}\n")

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "func Bar"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "bar.go")
	assert.NotContains(t, got.Output, "baz.go")
}

func TestExecuteQuery_Grep_ScopedToPathGlob(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/foo/bar.go", "TODO: fix this\n")
	writeRepoFile(t, repo, "internal/other/baz.go", "TODO: fix that\n")

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "TODO", PathGlob: "internal/foo"})
	assert.Contains(t, got.Output, "bar.go")
	assert.NotContains(t, got.Output, "baz.go")
}

// A model naturally writes a GLOB into a field named path_glob ("**/*.go").
// Live regression: treating that string as a literal directory silently
// walked nothing and reported "(no matches)" — which the model then read as
// positive evidence and returned a confident, WRONG not-fired. A glob must
// actually filter files, and a scope that matches nothing on disk must never
// masquerade as a searched-and-empty result.
func TestExecuteQuery_Grep_PathGlobIsAGlob(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/foo/bar.go", "SENTINEL here\n")
	writeRepoFile(t, repo, "internal/foo/notes.txt", "SENTINEL there\n")

	goOnly := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "SENTINEL", PathGlob: "**/*.go"})
	assert.Empty(t, goOnly.Err)
	assert.Contains(t, goOnly.Output, "bar.go")
	assert.NotContains(t, goOnly.Output, "notes.txt")

	txtOnly := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "SENTINEL", PathGlob: "**/*.txt"})
	assert.Empty(t, txtOnly.Err)
	assert.Contains(t, txtOnly.Output, "notes.txt")
	assert.NotContains(t, txtOnly.Output, "bar.go")
}

// The safety-critical half of the same regression: a scope naming something
// that does not exist is an ERROR, never an empty "(no matches)" — a query
// that never ran must not read as evidence of absence.
func TestExecuteQuery_Grep_NonexistentScopeIsAnErrorNotAnEmptyResult(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/foo/bar.go", "SENTINEL here\n")

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "SENTINEL", PathGlob: "no/such/dir"})
	assert.NotEmpty(t, got.Err, "a scope that matches nothing on disk must be an error, not a silent no-match")
	assert.Empty(t, got.Output)
}

// THE load-bearing one. Live regression: the repo held 208k files (159k of
// them gitignored worktree junk), the scan blew its file cap after 2k, aborted
// the walk, found nothing — and reported a bare "(no matches)". The model read
// that as positive evidence of absence and returned a CONFIDENT not-fired on a
// trigger whose sentinel was sitting right there on disk. A search that did not
// finish must never be able to say "no matches"; a truncated zero-match scan is
// INCONCLUSIVE, full stop.
func TestQueryGrep_TruncatedScanIsInconclusiveNotAnEmptyResult(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i < 12; i++ {
		writeRepoFile(t, repo, filepath.Join("pad", fmt.Sprintf("f%02d.go", i)), "package pad\n")
	}
	writeRepoFile(t, repo, "zzz_last/needle.go", "SENTINEL_VALUE\n")

	// A budget far too small to reach the needle: the walk must bail out.
	got := queryGrep(repo, triggers.Query{Type: triggers.QueryGrep, Pattern: "SENTINEL_VALUE"}, grepBudget{maxFilesScanned: 3, maxMatches: 20, maxFileReadBytes: 1 << 20})
	assert.Empty(t, got.Output, "a truncated scan must not present itself as a completed search")
	assert.NotEmpty(t, got.Err, "a truncated, zero-match scan is inconclusive — it must NOT read as 'no matches'")
	assert.Contains(t, strings.ToLower(got.Err), "truncated")
	assert.Contains(t, strings.ToLower(got.Err), "inconclusive")
}

// The counterpart: a scan that COMPLETES and genuinely finds nothing is a real
// "no matches" — that is legitimate positive evidence and must stay available.
func TestQueryGrep_CompletedScanWithNoHitsIsARealNoMatch(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "a.go", "package a\n")

	got := queryGrep(repo, triggers.Query{Type: triggers.QueryGrep, Pattern: "NOPE"}, defaultGrepBudget())
	assert.Empty(t, got.Err, "a completed exhaustive scan that found nothing is a valid result, not an error")
	assert.Empty(t, got.Output)
}

// U088-F02: a literal (non-glob) path_glob naming a hidden directory (e.g.
// ".github") must actually be searched. skipGrepDir exists to keep tool/
// vendor state encountered while DESCENDING out of the search, but it used
// to be applied to the walk ROOT too — so a scope naming a hidden directory
// by name walked ZERO files and reported a plain empty "(no matches)",
// which reads to the model as positive evidence of absence for a search
// that never actually ran.
func TestQueryGrep_LiteralScopeNamingAHiddenDirectoryIsSearched(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, ".github/workflows/ci.yml", "SENTINEL_SETTING: true\n")

	got := queryGrep(repo, triggers.Query{Type: triggers.QueryGrep, Pattern: "SENTINEL_SETTING", PathGlob: ".github"}, defaultGrepBudget())
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "ci.yml", "a literal path_glob naming a hidden directory must actually be searched, not silently voided")
}

// U088-F14: maxGrepFilesScanned is documented as a bound on WORK ("a
// zero-match pattern over a huge tree still terminates promptly"), but the
// old scanned counter only incremented AFTER the scope filter — so a glob
// matching nothing walked every file in the tree before the budget check
// ever fired. A tiny budget over a tree bigger than it proves the walk
// itself now stops early (reported as truncated/inconclusive) instead of
// silently completing a full unbounded walk.
func TestQueryGrep_BudgetBoundsTheWalkNotJustMatchedFiles(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i < 20; i++ {
		writeRepoFile(t, repo, filepath.Join("pkg", fmt.Sprintf("f%02d.go", i)), "package pkg\n")
	}

	got := queryGrep(repo, triggers.Query{Type: triggers.QueryGrep, Pattern: "anything", PathGlob: "**/*.rs"},
		grepBudget{maxFilesScanned: 5, maxMatches: 20, maxFileReadBytes: 1 << 20})
	assert.Empty(t, got.Output)
	assert.NotEmpty(t, got.Err, "a walk that hit its file-visited bound before finishing must be inconclusive")
	assert.Contains(t, strings.ToLower(got.Err), "truncated", "the budget must bound files WALKED, not just files that passed the scope filter")
}

// Tool/vendor state is not "the codebase": .git, .claude (worktrees!), and
// node_modules must be skipped, so they neither pollute results nor burn the
// scan budget that the real source tree needs.
func TestQueryGrep_SkipsToolStateAndVendorDirs(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/real.go", "REAL_HIT\n")
	writeRepoFile(t, repo, ".claude/worktrees/agent-x/copy.go", "REAL_HIT\n")
	writeRepoFile(t, repo, "node_modules/pkg/index.js", "REAL_HIT\n")

	got := queryGrep(repo, triggers.Query{Type: triggers.QueryGrep, Pattern: "REAL_HIT"}, defaultGrepBudget())
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "internal/real.go")
	assert.NotContains(t, got.Output, "worktrees", "gitignored worktree copies are not the codebase")
	assert.NotContains(t, got.Output, "node_modules")
}

// os.ReadFile FOLLOWS a symlink even though fs.WalkDir does not descend into
// one. Without skipping non-regular files, a symlink planted in the repo
// would pull outside-repo content straight into the model's prompt.
func TestExecuteQuery_Grep_DoesNotReadThroughASymlinkOutOfTheRepo(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("SUPERSECRET value\n"), 0o644))

	if err := os.Symlink(secret, filepath.Join(repo, "innocent.txt")); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "SUPERSECRET"})
	assert.NotContains(t, got.Output, "SUPERSECRET", "grep must not read through a symlink that escapes the repo")
}

func TestExecuteQuery_Grep_NoMatchesIsNotAnError(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "internal/foo/bar.go", "package foo\n")

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "NoSuchSymbolAnywhere"})
	assert.Empty(t, got.Err)
	assert.Empty(t, got.Output)
}

// Go's regexp is RE2-based (linear time, no catastrophic backtracking), so an
// adversarial pattern cannot hang the query — this proves a pathological
// pattern still returns promptly rather than being rejected outright (it's
// safe to just run).
func TestExecuteQuery_Grep_PathologicalPatternDoesNotHang(t *testing.T) {
	repo := t.TempDir()
	writeRepoFile(t, repo, "big.go", strings.Repeat("aaaaaaaaaa", 500)+"\n")

	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "(a+)+b"})
	assert.Empty(t, got.Err)
}

func TestExecuteQuery_Grep_InvalidPatternIsAnError(t *testing.T) {
	repo := t.TempDir()
	got := executeQuery(context.Background(), &git.Fake{}, repo, nil, triggers.Query{Type: triggers.QueryGrep, Pattern: "(unterminated["})
	assert.NotEmpty(t, got.Err)
}

func TestExecuteQuery_GitLogPath_FiltersToMatchingCommits(t *testing.T) {
	repo := t.TempDir()
	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{
		repo: {
			{SHA: "aaaa1111", Subject: "touch signing", Files: []string{"internal/signing/cli.go"}},
			{SHA: "bbbb2222", Subject: "touch unrelated", Files: []string{"internal/other/x.go"}},
		},
	}}
	got := executeQuery(context.Background(), gitFake, repo, nil, triggers.Query{Type: triggers.QueryGitLogPath, Path: "internal/signing"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "touch signing")
	assert.NotContains(t, got.Output, "touch unrelated")
}

func TestExecuteQuery_GitLogPath_NoMatchesSaysSo(t *testing.T) {
	repo := t.TempDir()
	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{repo: {{SHA: "a", Subject: "x", Files: []string{"unrelated.go"}}}}}
	got := executeQuery(context.Background(), gitFake, repo, nil, triggers.Query{Type: triggers.QueryGitLogPath, Path: "internal/signing"})
	assert.Empty(t, got.Err)
	assert.NotEmpty(t, got.Output)
}

// U088-F07: queryGitLogPath scans only the most recent gitLogPathScanCap
// commits (via LogSince). A zero-match result from a window that hit that
// cap did not finish looking, so it must not read as a confident "no commits
// found touching this path" — the exact hazard queryGrep already guards
// against for its own file-count bound.
func TestExecuteQuery_GitLogPath_TruncatedWindowIsInconclusive(t *testing.T) {
	repo := t.TempDir()
	entries := make([]git.LogEntry, gitLogPathScanCap)
	for i := range entries {
		entries[i] = git.LogEntry{SHA: fmt.Sprintf("sha%d", i), Subject: "unrelated change", Files: []string{"unrelated.go"}}
	}
	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{repo: entries}}

	got := executeQuery(context.Background(), gitFake, repo, nil, triggers.Query{Type: triggers.QueryGitLogPath, Path: "internal/signing"})
	assert.Empty(t, got.Output, "a truncated window must not present a completed, confident search")
	assert.NotEmpty(t, got.Err, "a fully-truncated commit window with zero matches is inconclusive, not evidence of absence")
}

// U053-F13: a commit whose changed-file list could not be gathered
// (FilesUnknown) is not a commit that touched nothing. Filtering it out
// silently turns "we never found out" into "it did not touch the path", so a
// zero-match result over such a window is inconclusive, exactly like the
// truncated window above.
func TestExecuteQuery_GitLogPath_UnknownFileListIsInconclusive(t *testing.T) {
	repo := t.TempDir()
	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{repo: {
		{SHA: "aaaa1111", Subject: "unrelated change", Files: []string{"unrelated.go"}},
		{SHA: "bbbb2222", Subject: "file list unavailable", FilesUnknown: true},
	}}}

	got := executeQuery(context.Background(), gitFake, repo, nil, triggers.Query{Type: triggers.QueryGitLogPath, Path: "internal/signing"})
	assert.Empty(t, got.Output, "a search that could not read every commit must not present a completed result")
	assert.NotEmpty(t, got.Err, "zero matches over a window with an unreadable commit is inconclusive, not evidence of absence")
}

// A MATCH found elsewhere in the window still answers the question, even when
// another commit's file list was unavailable — the inconclusive guard must
// not suppress evidence that was actually found.
func TestExecuteQuery_GitLogPath_UnknownFileListDoesNotSuppressAMatch(t *testing.T) {
	repo := t.TempDir()
	gitFake := &git.Fake{LogEntries: map[string][]git.LogEntry{repo: {
		{SHA: "aaaa1111", Subject: "touch signing", Files: []string{"internal/signing/cli.go"}},
		{SHA: "bbbb2222", Subject: "file list unavailable", FilesUnknown: true},
	}}}

	got := executeQuery(context.Background(), gitFake, repo, nil, triggers.Query{Type: triggers.QueryGitLogPath, Path: "internal/signing"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "touch signing")
}

func TestExecuteQuery_TaskStatus_Found(t *testing.T) {
	byHarp := map[string]tasks.Task{"other-task": {HarpID: "other-task", Status: "Done", Text: "shipped it"}}
	got := executeQuery(context.Background(), &git.Fake{}, "", byHarp, triggers.Query{Type: triggers.QueryTaskStatus, HarpID: "other-task"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "Done")
}

func TestExecuteQuery_TaskStatus_NotFound(t *testing.T) {
	got := executeQuery(context.Background(), &git.Fake{}, "", map[string]tasks.Task{}, triggers.Query{Type: triggers.QueryTaskStatus, HarpID: "ghost"})
	assert.Empty(t, got.Err)
	assert.Contains(t, got.Output, "no task")
}

// executeQuery re-validates every query itself (defense in depth: a caller
// must not be able to skip sanitize-then-execute by calling this directly).
func TestExecuteQuery_RevalidatesAndRejectsUnsafeInput(t *testing.T) {
	repo := t.TempDir()
	cases := []triggers.Query{
		{Type: triggers.QueryPathExists, Path: "../../../etc/passwd"},
		{Type: triggers.QueryPathExists, Path: "/etc/passwd"},
		{Type: "shell_exec", Path: "x"},
		{Type: triggers.QueryGrep, PathGlob: "../../etc"},
	}
	for _, q := range cases {
		got := executeQuery(context.Background(), &git.Fake{}, repo, nil, q)
		assert.NotEmpty(t, got.Err, "%+v must be rejected", q)
	}
}

func TestExecuteQuery_NeverPanicsOnGarbledInput(t *testing.T) {
	repo := t.TempDir()
	garbled := []triggers.Query{
		{},
		{Type: triggers.QueryPathExists},
		{Type: triggers.QueryGrep},
		{Type: triggers.QueryGitLogPath},
		{Type: triggers.QueryTaskStatus},
		{Type: triggers.QueryPathExists, Path: strings.Repeat("a/", 10000)},
	}
	assert.NotPanics(t, func() {
		for _, q := range garbled {
			executeQuery(context.Background(), &git.Fake{}, repo, nil, q)
		}
	})
}

func TestExecuteQueries_RespectsBatchBudget(t *testing.T) {
	repo := t.TempDir()
	qs := []triggers.Query{
		{Type: triggers.QueryPathExists, Path: "a"},
		{Type: triggers.QueryPathExists, Path: "b"},
		{Type: triggers.QueryPathExists, Path: "c"},
	}
	budget := 2
	results := executeQueries(context.Background(), &git.Fake{}, repo, nil, qs, &budget)
	require.Len(t, results, 3, "one result per query, even budget-exhausted ones")
	assert.Empty(t, results[0].Err)
	assert.Empty(t, results[1].Err)
	assert.NotEmpty(t, results[2].Err, "the third query exceeds the batch budget")
	assert.Equal(t, 0, budget)
}
