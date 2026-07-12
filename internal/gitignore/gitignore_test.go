package gitignore

import (
	"os"
	"path/filepath"
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
