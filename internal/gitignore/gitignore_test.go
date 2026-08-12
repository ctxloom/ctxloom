package gitignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
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

// TestPrivateStatePatterns_MatchExpectedSet pins membership EXACTLY, in both
// directions. A pattern missing from the set leaks private state; a pattern
// present for a path no writer produces is worse than useless — it documents a
// directory that does not exist, and a reader auditing "what does ctxloom keep
// out of git" is told about a tier that was never built. `.ctxloom/pieces/`
// (the L4 sparse-checkout fetcher, never built) and `.ctxloom/ephemeral/` (the
// ephemeral concept is permanently HOME-rooted — paths.HarpEphemeralDir) were
// both phantoms of that second kind.
func TestPrivateStatePatterns_MatchExpectedSet(t *testing.T) {
	assert.ElementsMatch(t, []string{
		".ctxloom/cache/",
		".ctxloom/sessions/",
		".ctxloom/project-id",
		".ctxloom/state/",
	}, PrivateStatePatterns)
}

// TestWorktreeArtifactPatterns_MatchExpectedSet pins that this is the set
// whose incompleteness has twice been implicated in destroying agent work: a
// ctxloom-written file missing from it stays visible to `git status` inside a
// per-agent worktree, which false-dirties the worktree and makes teardown
// (correctly) refuse to remove it — orphaning it permanently — or, in the
// mirror case, rides an agent's merge-back. PrivateStatePatterns, whose worst
// failure is a noisy diff, had an exact-membership pin; this one had two spot
// checks. Membership is the invariant, so membership is what is pinned.
func TestWorktreeArtifactPatterns_MatchExpectedSet(t *testing.T) {
	assert.ElementsMatch(t, []string{
		".mcp.json",
		".claude/",
		".agents/",
		".codex/config.toml",
		".codex/auth.json",
		".kiro/",
		".ctxloom/cache/",
		"CLAUDE.md",
		"AGENTS.md",
		".opencode/",
		"opencode.json",
		ledger.Name,
	}, WorktreeArtifactPatterns)
}

// TestWorktreeArtifactPatterns_CoverTransientOnes pins that the two sets
// are documented as standing in a definite relationship — WorktreeArtifact is
// "the BROADENED set … must keep EVERY ctxloom-written config out of a
// developer member's merge-back" — but nothing enforced it, and the sets had
// in fact drifted: the per-file hook backups (since retired) were ignored in
// the project .gitignore and NOT hidden inside a per-agent worktree, where
// hook application writes them just the same. The BROADENING direction is by
// design (.mcp.json, .claude/ and CLAUDE.md are deliberately the project's
// choice in a plain checkout); the containment direction is the invariant.
func TestWorktreeArtifactPatterns_CoverTransientOnes(t *testing.T) {
	for _, p := range TransientArtifactPatterns {
		assert.Contains(t, WorktreeArtifactPatterns, p,
			"every artifact ctxloom keeps out of a plain checkout must also be kept out of a per-agent worktree's merge-back")
	}
}

// TestPatternSets_AreNonEmpty pins against a pattern list arriving empty. Every production call site of Ensure /
// EnsureFile passes one of these package-level sets verbatim, so an empty list
// is not something a caller can construct — but if one of these sets were ever
// emptied, the failure would be silent twice over: EnsureFile returns nil for
// an empty list ("ensured" nothing), and git.ListTracked treats an empty
// pathspec list as "match nothing", so isolation's skip-worktree merge-isolation
// pass (which passes WorktreeArtifactPatterns as its pathspecs) would quietly
// become a no-op rather than failing.
func TestPatternSets_AreNonEmpty(t *testing.T) {
	assert.NotEmpty(t, PrivateStatePatterns)
	assert.NotEmpty(t, TransientArtifactPatterns)
	assert.NotEmpty(t, WorktreeArtifactPatterns,
		"isolation passes this as git.ListTracked's pathspec list, where empty means MATCH NOTHING")
	assert.NotEmpty(t, SupersededPatterns)
}

// TestTransientArtifactPatterns_IgnoreCodexCredential pins that
// internal/lm/isolation/auth.go's SeedCodexHome actively copies the
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

// TestArtifactPatterns_GranularityRule pins that the file-granular vs
// directory-granular choice is made per entry, and broadening one to its whole
// directory is irreversible in one direction: it un-tracks whatever the
// project had already committed there, silently. .codex/ holds a user's own
// files alongside the two ctxloom writes, so it must stay named file by file
// — the same reasoning .agents/ fails, which is why THAT one is wholesale.
func TestArtifactPatterns_GranularityRule(t *testing.T) {
	for _, set := range [][]string{TransientArtifactPatterns, WorktreeArtifactPatterns} {
		assert.NotContains(t, set, ".codex/",
			"a project's own .codex files must not be swept up; name the ctxloom-written ones individually")
		assert.Contains(t, set, ".codex/config.toml")
		assert.Contains(t, set, ".codex/auth.json")
		assert.Contains(t, set, ".agents/",
			"everything under .agents/ is machine-written, so it is ignored wholesale")
	}
}

// TestWorktreeArtifactPatterns_IncludesOpencodeArtifacts pins that
// WorktreeArtifactPatterns is documented as "the FULL written set across all
// engines", but opencode's own written artifacts (.opencode/ command+skill+
// context files, opencode.json, and the shared managed-content marker
// ledger) were absent, so a per-agent worktree run using the opencode backend
// left untracked ctxloom files the exclude block did not hide.
func TestWorktreeArtifactPatterns_IncludesOpencodeArtifacts(t *testing.T) {
	assert.Contains(t, WorktreeArtifactPatterns, ".opencode/",
		"opencode's command/skill/context files under .opencode/ must be kept out of merge-back")
	assert.Contains(t, WorktreeArtifactPatterns, "opencode.json",
		"opencode's project-local managed config must be kept out of merge-back")
	assert.Contains(t, WorktreeArtifactPatterns, ledger.Name,
		"the shared managed-content marker must be kept out of merge-back")
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
		".ctxloom/sessions/",
		".ctxloom/project-id",
		".ctxloom/state/",
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

// TestEnsure_LeavesRetiredPhantomPatternsAlone is the other half of removing a
// pattern from PrivateStatePatterns: every project initialized by an older
// ctxloom already has `.ctxloom/pieces/` and `.ctxloom/ephemeral/` written into
// its .gitignore, and those lines are the USER'S file now. Ensure appends and
// retires only the superseded blanket rule, so ceasing to write a pattern must
// leave existing ones exactly where they are — untouched, inert, and nobody's
// merge conflict.
func TestEnsure_LeavesRetiredPhantomPatternsAlone(t *testing.T) {
	dir := t.TempDir()
	pre := ".ctxloom/cache/\n.ctxloom/pieces/\n.ctxloom/sessions/\n.ctxloom/ephemeral/\n.ctxloom/project-id\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(pre), 0644))

	require.NoError(t, Ensure(dir, Comment, PrivateStatePatterns...))

	got := readGitignore(t, dir)
	for _, retired := range []string{".ctxloom/pieces/", ".ctxloom/ephemeral/"} {
		assert.Equal(t, 1, countOccurrences(got, "\n"+retired+"\n"),
			"a pattern ctxloom no longer writes must be neither removed nor duplicated: %s", retired)
	}
	for _, p := range PrivateStatePatterns {
		assert.Contains(t, got, p, "the current patterns must still be ensured: %s", p)
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

	changed, err := RetireSupersededFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.True(t, changed, "retiring a blanket .ctxloom/ rule must report a change")

	got := readGitignore(t, dir)
	assert.NotContains(t, got, "\n.ctxloom/\n", "the blanket rule must be gone")
	assert.NotContains(t, got, "# ctxloom local files", "its orphaned header must go too")
	assert.Contains(t, got, ".DS_Store", "unrelated user entries must survive")
	assert.Contains(t, got, "# OS files", "unrelated user comments must survive")
}

// TestRetireSuperseded_RetiresEveryBlanketSpelling pins that retirement
// matched four literal spellings, but a blanket .ctxloom exclusion has more
// than four ways to be written and every one of them has the same effect: the
// project's own .ctxloom/content/ becomes invisible to git, `git add` reports
// nothing, and a content repo publishes an empty tree while every consumer's
// bundle refs fail to resolve. A spelling the migration does not recognise is
// a project it silently leaves broken — and Ensure only ever APPENDS, so no
// amount of re-running repairs it.
func TestRetireSuperseded_RetiresEveryBlanketSpelling(t *testing.T) {
	for _, blanket := range []string{
		".ctxloom", ".ctxloom/", "/.ctxloom", "/.ctxloom/",
		".ctxloom/*", ".ctxloom/**", "/.ctxloom/*", "/.ctxloom/**",
		"**/.ctxloom/", "**/.ctxloom",
		"  .ctxloom/  ",
	} {
		t.Run(blanket, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			require.NoError(t, os.WriteFile(path, []byte("# OS files\n.DS_Store\n"+blanket+"\n"), 0644))

			changed, err := RetireSupersededFile(path)
			require.NoError(t, err)
			assert.True(t, changed, "%q blanket-excludes .ctxloom/content/ and must be retired", blanket)
			assert.NotContains(t, readGitignore(t, dir), ".ctxloom")
			assert.Contains(t, readGitignore(t, dir), ".DS_Store", "unrelated user entries survive")
		})
	}
}

// TestRetireSuperseded_LeavesNonBlanketLinesAlone is the over-matching guard
// for the broadened matcher: current granular rules, a user's own re-include,
// and a comment mentioning the path must all survive untouched.
func TestRetireSuperseded_LeavesNonBlanketLinesAlone(t *testing.T) {
	for _, keep := range []string{
		".ctxloom/cache/", ".ctxloom/sessions/", ".ctxloom/project-id",
		".ctxloom/content/drafts/", ".ctxloomer/", ledger.Name,
		"!.ctxloom/", "!/.ctxloom/**",
		"# .ctxloom/",
	} {
		t.Run(keep, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			original := keep + "\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0644))

			changed, err := RetireSupersededFile(path)
			require.NoError(t, err)
			assert.False(t, changed, "%q is not a blanket .ctxloom exclusion", keep)
			assert.Equal(t, original, readGitignore(t, dir))
		})
	}
}

// TestRetireSuperseded_PreservesGranularRules guards against over-matching: the
// current granular private-state patterns are all prefixed .ctxloom/ and must
// NOT be mistaken for the blanket rule.
func TestRetireSuperseded_PreservesGranularRules(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte(".ctxloom/cache/\n.ctxloom/sessions/\n.ctxloom/project-id\n"), 0644))

	changed, err := RetireSupersededFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.False(t, changed, "granular patterns are current, not superseded")

	got := readGitignore(t, dir)
	for _, p := range PrivateStatePatterns {
		if p == ".ctxloom/cache/" || p == ".ctxloom/sessions/" || p == ".ctxloom/project-id" {
			assert.Contains(t, got, p)
		}
	}
}

// TestRetireSuperseded_PreservesFileMode REFUTES the claim that retirement
// "resets its mode": os.WriteFile
// applies its perm argument only when CREATING a file, so an existing
// .gitignore's mode was never touched. The assertion is kept because the
// atomic rewrite below WOULD reset it — a fresh temp file takes the umask,
// and renaming it over the original silently re-modes a user-authored file.
func TestRetireSuperseded_PreservesFileMode(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0640, 0664} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			require.NoError(t, os.WriteFile(path, []byte("# ctxloom local files\n.ctxloom/\n.DS_Store\n"), mode))
			require.NoError(t, os.Chmod(path, mode), "umask must not decide what this test asserts")

			changed, err := RetireSupersededFile(path)
			require.NoError(t, err)
			require.True(t, changed)

			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, mode, info.Mode().Perm(),
				"a user-authored file's mode is the user's, not ctxloom's to normalize")
		})
	}
}

// TestRetireSuperseded_LeavesNoTempFile pins the visible half of the atomic
// rewrite. The crash window itself — a truncate-then-write that loses the
// user's whole .gitignore if the process dies between the two — cannot be
// discriminated by a unit test without injecting a crash, so this asserts
// what IS observable: the replacement is staged beside the target and leaves
// nothing behind.
func TestRetireSuperseded_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("# ctxloom local files\n.ctxloom/\n.DS_Store\n"), 0644))

	changed, err := RetireSupersededFile(path)
	require.NoError(t, err)
	require.True(t, changed)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the staged replacement must not survive the write")
	assert.Equal(t, ".gitignore", entries[0].Name())
	assert.Contains(t, readGitignore(t, dir), ".DS_Store")
}

// TestRetireSuperseded_Idempotent pins that a clean file is left untouched.
func TestRetireSuperseded_Idempotent(t *testing.T) {
	dir := t.TempDir()
	original := "# OS files\n.DS_Store\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(original), 0644))

	changed, err := RetireSupersededFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, original, readGitignore(t, dir), "a file with nothing to retire must not be rewritten")
}

// TestRetireSuperseded_MissingFile is a no-op, not an error: a project may have
// no .gitignore at all.
func TestRetireSuperseded_MissingFile(t *testing.T) {
	changed, err := RetireSupersededFile(filepath.Join(t.TempDir(), ".gitignore"))
	require.NoError(t, err)
	assert.False(t, changed)
}

// TestRetireWorktreeConfigBlock_RemovesHeaderAndPatterns pins the retirement
// half: the shared common-dir info/exclude block must be
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

	_, err := RetireSupersededFile(filepath.Join(dir, ".gitignore"))
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

// TestEnsureFile_WarnsWhenAppendingOverAUserNegation pins that .gitignore
// is LAST-MATCH-WINS and Ensure only ever appends at the end of the file, so a
// pattern ctxloom appends silently overrides a user's earlier `!` re-include
// of the same path — against the package doc's promise to append "without
// disturbing user entries".
//
// Reordering is not the remedy and the row's framing overstates what one is
// available: git cannot re-include a path whose PARENT DIRECTORY is excluded
// at all, so for a directory pattern like .ctxloom/cache/ the user's negation
// is dead however the file is ordered. What ctxloom can do is stop doing it
// silently — the user's line is still there, still looks effective, and only
// a warning tells them it no longer is.
func TestEnsureFile_WarnsWhenAppendingOverAUserNegation(t *testing.T) {
	var warnings strings.Builder
	restore := clidiag.SetSink(&warnings)
	defer restore()

	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("# keep my scratch notes\n!.ctxloom/cache/notes.md\n"), 0644))

	require.NoError(t, EnsureFile(path, testComment, ".ctxloom/cache/"))

	got := warnings.String()
	assert.Contains(t, got, ".ctxloom/cache/", "the warning must name the pattern ctxloom added")
	assert.Contains(t, got, "!.ctxloom/cache/notes.md", "and the user line it overrides")

	// The user's line survives — this warns, it never edits user entries.
	assert.Contains(t, readGitignore(t, dir), "!.ctxloom/cache/notes.md")
}

// TestEnsureFile_DoesNotWarnWithoutAnAffectedNegation is the over-warning
// guard: an unrelated negation, or one the appended pattern cannot possibly
// shadow, must stay silent.
func TestEnsureFile_DoesNotWarnWithoutAnAffectedNegation(t *testing.T) {
	var warnings strings.Builder
	restore := clidiag.SetSink(&warnings)
	defer restore()

	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("*.log\n!important.log\n!src/vendor/keep.go\n"), 0644))

	require.NoError(t, EnsureFile(path, testComment, ".ctxloom/cache/", ".agents/"))

	assert.Empty(t, warnings.String(), "an unrelated re-include is none of ctxloom's business")
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

// TestCloseChecked_PropagatesCloseError pins that appendBlock's old
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
