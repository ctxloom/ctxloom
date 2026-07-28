package gitignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testComment = "# ctxloom private working state"

// readGitignore returns the .gitignore contents for dir, or "" if absent.
func readGitignore(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// readPlainFile reads an arbitrary file's contents as a string, failing the
// test on error (unlike readGitignore, which treats absence as "").
func readPlainFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestEnsure_CreatesFileWithPatterns(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, Ensure(dir, testComment, ".ctxloom/ephemeral/", ".ctxloom/project-id"))

	got := readGitignore(t, dir)
	assert.Contains(t, got, testComment)
	assert.Contains(t, got, ".ctxloom/ephemeral/")
	assert.Contains(t, got, ".ctxloom/project-id")
}

func TestEnsure_AppendsOnlyMissingPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("node_modules/\n.ctxloom/ephemeral/\n"), 0644))

	require.NoError(t, Ensure(dir, testComment, ".ctxloom/ephemeral/", ".agents/"))

	got := readGitignore(t, dir)
	// The already-present pattern is not duplicated.
	assert.Equal(t, 1, countOccurrences(got, ".ctxloom/ephemeral/"))
	// The missing pattern is appended.
	assert.Contains(t, got, ".agents/")
	// User content is preserved.
	assert.Contains(t, got, "node_modules/")
}

func TestEnsure_NoMissingPatternsLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	original := "node_modules/\n.agents/\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0644))

	require.NoError(t, Ensure(dir, testComment, ".agents/"))

	assert.Equal(t, original, readGitignore(t, dir))
}

func TestEnsure_EmptyPatternsIsNoOp(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, Ensure(dir, testComment))

	_, err := os.Stat(filepath.Join(dir, ".gitignore"))
	assert.True(t, os.IsNotExist(err), "no .gitignore should be created")
}

func TestEnsure_InsertsSeparatorWhenFileLacksTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("node_modules/"), 0644))

	require.NoError(t, Ensure(dir, testComment, ".agents/"))

	got := readGitignore(t, dir)
	// The pre-existing line stays intact on its own line.
	assert.Contains(t, got, "node_modules/\n")
	assert.Contains(t, got, ".agents/")
}

func TestEnsure_IsIdempotentAcrossRuns(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, Ensure(dir, testComment, ".agents/", "*.ctxloom.bak"))
	first := readGitignore(t, dir)
	require.NoError(t, Ensure(dir, testComment, ".agents/", "*.ctxloom.bak"))
	second := readGitignore(t, dir)

	assert.Equal(t, first, second)
}

func TestPrivateStatePatterns_MatchExpectedSet(t *testing.T) {
	assert.ElementsMatch(t, []string{
		".ctxloom/cache/",
		".ctxloom/pieces/",
		".ctxloom/sessions/",
		".ctxloom/ephemeral/",
		".ctxloom/project-id",
	}, PrivateStatePatterns)
}

// TestTransientArtifactPatterns_IgnoreCodexCredential pins the second half of
// U045-F01: internal/lm/isolation/auth.go's SeedCodexHome actively copies the
// host's ~/.codex/auth.json into <workDir>/.codex/auth.json on every plain
// (non-isolated) codex run — a live credential landing directly in the
// project's tracked working tree. Only .codex/config.toml was ever ignored;
// the credential file sitting right next to it was not.
func TestTransientArtifactPatterns_IgnoreCodexCredential(t *testing.T) {
	assert.Contains(t, TransientArtifactPatterns, ".codex/auth.json",
		"the copied credential file must be gitignored exactly like .codex/config.toml is")
	assert.Contains(t, WorktreeArtifactPatterns, ".codex/auth.json",
		"a per-agent worktree fan-out member must also keep the credential out of merge-back")
}

// TestEnsure_InitBehavior_CommitsContentIgnoresPrivateState mirrors the call
// init.go makes (gitignore.Ensure(projectDir, gitignore.Comment,
// gitignore.PrivateStatePatterns...)) and asserts the resulting .gitignore
// ignores the rebuildable/local-state paths while leaving the
// content/config/trust paths committed-by-omission (never mentioned, so git
// tracks them by default).
func TestEnsure_InitBehavior_CommitsContentIgnoresPrivateState(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, Ensure(dir, Comment, PrivateStatePatterns...))

	got := readGitignore(t, dir)

	for _, ignored := range []string{
		".ctxloom/cache/",
		".ctxloom/pieces/",
		".ctxloom/sessions/",
		".ctxloom/ephemeral/",
		".ctxloom/project-id",
	} {
		assert.Contains(t, got, ignored, "expected %q to be ignored", ignored)
	}

	for _, committed := range []string{
		".ctxloom/content/",
		".ctxloom/config.yaml",
		".ctxloom/remotes.yaml",
		".ctxloom/lock.yaml",
		".ctxloom/allowed_signers",
		".ctxloom/approvals/",
	} {
		assert.NotContains(t, got, committed, "expected %q to stay committed (not ignored)", committed)
	}
}

func TestEnsure_InitBehavior_IsIdempotent(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, Ensure(dir, Comment, PrivateStatePatterns...))
	first := readGitignore(t, dir)
	require.NoError(t, Ensure(dir, Comment, PrivateStatePatterns...))
	second := readGitignore(t, dir)

	assert.Equal(t, first, second)
}

// countOccurrences counts non-overlapping occurrences of sub in s.
func countOccurrences(s, sub string) int {
	count := 0
	for i := 0; i+len(sub) <= len(s); {
		if s[i:i+len(sub)] == sub {
			count++
			i += len(sub)
			continue
		}
		i++
	}
	return count
}

// ==========================================================================
// RetireSuperseded — removing ignore rules that a newer layout invalidated
// ==========================================================================

// TestRetireSuperseded_RemovesBlanketCtxloomRule is the core regression. Older
// ctxloom wrote a blanket `.ctxloom/` ignore, which predates version-controlled
// content living INSIDE .ctxloom/content/. Left in place it silently un-tracks
// the project's own content: git add reports nothing and a content repo
// publishes an empty tree. Ensure only appends, so nothing could ever remove it.
func TestRetireSuperseded_RemovesBlanketCtxloomRule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("# OS files\n.DS_Store\n\n# ctxloom local files\n.ctxloom/\n"), 0644))

	changed, err := RetireSuperseded(dir)
	require.NoError(t, err)
	assert.True(t, changed, "retiring a blanket .ctxloom/ rule must report a change")

	got := readGitignore(t, dir)
	assert.NotContains(t, got, "\n.ctxloom/\n", "the blanket rule must be gone")
	assert.NotContains(t, got, "# ctxloom local files", "its orphaned header must go too")
	assert.Contains(t, got, ".DS_Store", "unrelated user entries must survive")
	assert.Contains(t, got, "# OS files", "unrelated user comments must survive")
}

// TestRetireSuperseded_PreservesGranularRules guards against over-matching: the
// current granular private-state patterns are all prefixed .ctxloom/ and must
// NOT be mistaken for the blanket rule.
func TestRetireSuperseded_PreservesGranularRules(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte(".ctxloom/cache/\n.ctxloom/sessions/\n.ctxloom/project-id\n"), 0644))

	changed, err := RetireSuperseded(dir)
	require.NoError(t, err)
	assert.False(t, changed, "granular patterns are current, not superseded")

	got := readGitignore(t, dir)
	for _, p := range PrivateStatePatterns {
		if p == ".ctxloom/cache/" || p == ".ctxloom/sessions/" || p == ".ctxloom/project-id" {
			assert.Contains(t, got, p)
		}
	}
}

// TestRetireSuperseded_Idempotent pins that a clean file is left untouched.
func TestRetireSuperseded_Idempotent(t *testing.T) {
	dir := t.TempDir()
	original := "# OS files\n.DS_Store\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(original), 0644))

	changed, err := RetireSuperseded(dir)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, original, readGitignore(t, dir), "a file with nothing to retire must not be rewritten")
}

// TestRetireSuperseded_MissingFile is a no-op, not an error: a project may have
// no .gitignore at all.
func TestRetireSuperseded_MissingFile(t *testing.T) {
	changed, err := RetireSuperseded(t.TempDir())
	require.NoError(t, err)
	assert.False(t, changed)
}


// TestRetireWorktreeConfigBlock_RemovesHeaderAndPatterns pins the retirement
// half of U054-F02: the shared common-dir info/exclude block must be
// removable once no worktree still needs it, and removal must not touch
// anything else in the file.
func TestRetireWorktreeConfigBlock_RemovesHeaderAndPatterns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exclude")
	content := "*.log\n\n" + WorktreeComment + "\n" + strings.Join(WorktreeArtifactPatterns, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	changed, err := RetireWorktreeConfigBlock(path)
	require.NoError(t, err)
	assert.True(t, changed)

	got := readPlainFile(t, path)
	assert.Contains(t, got, "*.log", "unrelated pre-existing content survives")
	assert.NotContains(t, got, WorktreeComment)
	for _, p := range WorktreeArtifactPatterns {
		assert.NotContains(t, got, p)
	}
}

// TestRetireWorktreeConfigBlock_MissingFile mirrors
// TestRetireSuperseded_MissingFile: an absent file is a no-op, not an error.
func TestRetireWorktreeConfigBlock_MissingFile(t *testing.T) {
	changed, err := RetireWorktreeConfigBlock(filepath.Join(t.TempDir(), "exclude"))
	require.NoError(t, err)
	assert.False(t, changed)
}

// TestRetireSuperseded_ThenEnsure_UnignoresContent pins the end-to-end repair an
// old project needs: after retire+Ensure, private state stays ignored while
// .ctxloom/content/ (and config/lock alongside it) becomes trackable again.
func TestRetireSuperseded_ThenEnsure_UnignoresContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("# ctxloom local files\n.ctxloom/\n"), 0644))

	_, err := RetireSuperseded(dir)
	require.NoError(t, err)
	require.NoError(t, Ensure(dir, Comment, PrivateStatePatterns...))

	got := readGitignore(t, dir)
	assert.NotContains(t, got, "\n.ctxloom/\n", "content must no longer be blanket-ignored")
	for _, p := range PrivateStatePatterns {
		assert.Contains(t, got, p, "private state must still be ignored: %s", p)
	}
}

// TestEnsure_RetiringBlanketRuleReplacesPrivateStateRules guards the leak that
// retirement could otherwise cause. The hook path calls Ensure with ONLY
// TransientArtifactPatterns. The blanket .ctxloom/ rule it retires was the very
// thing keeping cache/ and sessions/ out of git, so Ensure must install the
// granular private-state replacement even when the caller never asked for it —
// otherwise repairing the content bug leaks private working state into the repo.
func TestEnsure_RetiringBlanketRuleReplacesPrivateStateRules(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte("# ctxloom local files\n.ctxloom/\n"), 0644))

	// Exactly what internal/operations/hooks.go passes.
	require.NoError(t, Ensure(dir, Comment, TransientArtifactPatterns...))

	got := readGitignore(t, dir)
	assert.NotContains(t, got, "\n.ctxloom/\n", "the blanket rule must be retired")
	for _, p := range PrivateStatePatterns {
		assert.Contains(t, got, p,
			"retiring the blanket rule must not leak private state: %s should still be ignored", p)
	}
	for _, p := range TransientArtifactPatterns {
		assert.Contains(t, got, p, "the caller's own patterns must still be written: %s", p)
	}
}

// TestEnsure_NoBlanketRule_DoesNotInjectPrivateState pins the converse: Ensure
// must not smuggle PrivateStatePatterns into projects that never had the
// superseded rule. Only a retirement triggers the replacement.
func TestEnsure_NoBlanketRule_DoesNotInjectPrivateState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# OS files\n.DS_Store\n"), 0644))

	require.NoError(t, Ensure(dir, Comment, TransientArtifactPatterns...))

	got := readGitignore(t, dir)
	assert.NotContains(t, got, ".ctxloom/cache/",
		"a project with no superseded rule must not have private-state patterns injected by a transient-only Ensure")
}


// TestCloseChecked_PropagatesCloseError pins U054-F04: appendBlock's old
// `defer func() { _ = f.Close() }()` discarded a write-never-reached-disk
// failure and reported success — the worst case being Ensure's migration
// path, which has already committed the REMOVAL of the superseded blanket
// rule before the replacement append runs, so a silently-failed Close left
// the project with FEWER ignore rules than before. Forcing a REAL ENOSPC is
// impractical in a portable unit test, so this drives the exact propagation
// path (closeChecked) via an already-closed *os.File, whose second Close
// reliably errors.
func TestCloseChecked_PropagatesCloseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	require.NoError(t, err)
	require.NoError(t, f.Close(), "first close must succeed")

	err = closeChecked(f, path)
	require.Error(t, err, "a second Close on an already-closed file must error, and closeChecked must surface it rather than discard it")
	assert.Contains(t, err.Error(), path, "the error must name the file, not just the bare OS error")
}
