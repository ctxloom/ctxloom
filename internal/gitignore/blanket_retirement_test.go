package gitignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blanketSpellings is every way a project's .gitignore can exclude the WHOLE
// .ctxloom directory. Git treats them identically and each one breaks the
// project the same way: .ctxloom/content/ becomes invisible, `git add` reports
// nothing, and a content repo publishes an empty tree.
//
// The list is shared by the predicate-level and the end-to-end tables below so
// the two layers can never drift apart in what they claim to cover. A spelling
// added here without the matcher handling it fails at BOTH layers at once,
// which is what tells a reader whether a future regression lives in the
// predicate or above it.
var blanketSpellings = []string{
	".ctxloom",
	".ctxloom/",
	"/.ctxloom",
	"/.ctxloom/",
	".ctxloom/*",
	".ctxloom/**",
	"/.ctxloom/*",
	"/.ctxloom/**",
	"**/.ctxloom",
	"**/.ctxloom/",
	"  .ctxloom/*  ",
}

// nonBlanketLines are lines that mention .ctxloom but must NEVER be retired.
// Over-retiring is strictly worse than under-retiring: dropping a granular
// PrivateStatePatterns rule un-ignores the cache, fetched pieces, session
// state and the private project-id marker, pushing them at the next commit.
// Dropping a user's own re-include or comment silently edits their file.
var nonBlanketLines = []string{
	".ctxloom/cache/",
	".ctxloom/pieces/",
	".ctxloom/sessions/",
	".ctxloom/ephemeral/",
	".ctxloom/project-id",
	".ctxloom/*.lock",
	".ctxloom/content/drafts/",
	".ctxloomer/",
	".ctxloom-opencode-managed",
	"internal/**/.ctxloom/",
	"!.ctxloom/",
	"!.ctxloom/plans/",
	"!/.ctxloom/**",
	"# .ctxloom/",
	"# the root `.ctxloom/*` above is anchored by its slash",
	"",
	"   ",
}

// TestIsSupersededBlanket_MatchesEverySpelling exercises the matcher DIRECTLY,
// one layer below RetireSupersededFile. The distinction matters for diagnosis:
// `manage gitignore install` was reported to print "Updated <path>" and leave a
// blanket `.ctxloom/*` in place, and the first question any such report raises
// is whether the predicate or something above it failed. Pinning the predicate
// on its own makes that answerable from a test name instead of a debugger.
func TestIsSupersededBlanket_MatchesEverySpelling(t *testing.T) {
	for _, blanket := range blanketSpellings {
		t.Run(blanket, func(t *testing.T) {
			assert.True(t, isSupersededBlanket(blanket),
				"%q excludes the whole .ctxloom directory and must be recognised as superseded", blanket)
		})
	}
}

// TestIsSupersededBlanket_RejectsNonBlanketLines is the over-match guard at the
// predicate layer. See nonBlanketLines for why this direction is the dangerous
// one.
func TestIsSupersededBlanket_RejectsNonBlanketLines(t *testing.T) {
	for _, keep := range nonBlanketLines {
		t.Run(keep, func(t *testing.T) {
			assert.False(t, isSupersededBlanket(keep),
				"%q is not a blanket .ctxloom exclusion and must survive retirement", keep)
		})
	}
}

// harnessPatterns is what `manage gitignore install` still appends to the ROOT
// .gitignore, via cli.ensureHarnessGitignore: the transient artifacts alone.
// The private-state tier no longer appears here — it is written to the nested
// .ctxloom/.gitignore instead — and that is the whole point of the split: these
// patterns name paths OUTSIDE .ctxloom/ (.agents/, .codex/auth.json), so no
// nested file could express them, while every private-state pattern is under
// .ctxloom/ and so must not be here.
func harnessPatterns() []string {
	return append([]string{}, TransientArtifactPatterns...)
}

// ensureHarness replays cli.ensureHarnessGitignore's two writes in its order.
// Keeping the sequence in one helper is what stops these tests from drifting
// into asserting a combination the product never actually performs.
func ensureHarness(t *testing.T, dir string) {
	t.Helper()
	_, err := EnsureNested(dir)
	require.NoError(t, err)
	require.NoError(t, Ensure(dir, TransientArtifactComment, harnessPatterns()...))
}

// TestEnsure_RetiresEveryBlanketSpelling_WhenNothingIsMissing is the end-to-end
// half of the table, in the exact shape that made the original report look like
// a silent no-op: a .gitignore that ALREADY contains every pattern Ensure would
// append, so the append path has nothing to do and retirement is the ONLY thing
// that can change the file. If retirement misses the spelling, the file comes
// back byte-identical while the command still exits 0 — which is precisely what
// was observed.
//
// Covering the spellings at this layer as well as at the predicate layer is not
// redundant: retirement runs inside Ensure, ahead of the append, and a change
// to that ordering (or an early return added in front of it) would leave the
// predicate tests green while every real migration silently stopped working.
func TestEnsure_RetiresEveryBlanketSpelling_WhenNothingIsMissing(t *testing.T) {
	for _, blanket := range blanketSpellings {
		t.Run(blanket, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			original := "# OS files\n.DS_Store\n\n# Local config\n" + blanket + "\n" +
				strings.Join(harnessPatterns(), "\n") + "\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0644))

			ensureHarness(t, dir)

			got := readGitignore(t, dir)
			assert.NotEqual(t, original, got,
				"a file carrying %q is not migrated, so Ensure must not leave it byte-identical", blanket)
			assert.NotContains(t, ignoreRules(got), strings.TrimSpace(blanket),
				"the blanket rule %q must be gone", blanket)
			assert.Contains(t, got, ".DS_Store", "unrelated user entries survive")
			assert.Contains(t, got, "# OS files", "unrelated user comments survive")
			// Retirement removed the rule that was keeping private state out of
			// git, so the replacement must land in the same migration — but in
			// the NESTED file now, never back into the root one.
			nested := ignoreRules(readNested(t, dir))
			for _, p := range NestedPatterns() {
				assert.Contains(t, nested, p,
					"retirement must leave private state ignored: %s", p)
			}
			assert.NotContains(t, ignoreRules(got), ".ctxloom/content/",
				"authored content is committed by omission, never re-ignored")
			assert.NotContains(t, nested, "/content",
				"authored content is committed by omission in the nested file too")
		})
	}
}

// TestEnsure_MigratesRealWorldBlanketFile replays the shape this repo's own
// pre-migration .gitignore had: the blanket under a hand-written header, a
// user's deliberate re-include beneath it, a nested-dir defence rule that also
// mentions .ctxloom, and the granular private-state rules already present
// further down (which is why nothing was left to append).
//
// The re-include is the load-bearing assertion. `!.ctxloom/plans/` is dead
// while the blanket stands — git cannot re-include a path whose parent
// directory is excluded — and it becomes live again only once the blanket is
// retired. Removing it instead would silently discard a user's decision.
func TestEnsure_MigratesRealWorldBlanketFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	original := strings.Join([]string{
		"# Local config",
		".ctxloom/*",
		"# ...but keep the design plans under version control",
		"!.ctxloom/plans/",
		"# Defense-in-depth: a NESTED .ctxloom under a package dir is pollution",
		"internal/**/.ctxloom/",
		"",
		".ctxloom/cache/",
		".ctxloom/pieces/",
		".ctxloom/sessions/",
		".ctxloom/ephemeral/",
		".ctxloom/project-id",
		"*.ctxloom.bak",
		".agents/",
		".codex/config.toml",
		".codex/auth.json",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(original), 0644))

	ensureHarness(t, dir)

	got := readGitignore(t, dir)
	rules := ignoreRules(got)

	assert.NotContains(t, rules, ".ctxloom/*", "the blanket must be retired")
	assert.Contains(t, rules, "!.ctxloom/plans/", "a user's own re-include is never retired")
	assert.Contains(t, rules, "internal/**/.ctxloom/", "the nested-dir defence rule is not a blanket")
	assert.Contains(t, got, "# Local config", "a user's own comment is never removed")
	assert.Contains(t, got, "# ...but keep the design plans under version control")
	for _, p := range harnessPatterns() {
		assert.Equal(t, 1, countOccurrences(got, "\n"+p+"\n"),
			"%q must be present exactly once after migration", p)
	}
}

// TestEnsure_SecondRunAfterMigrationIsAByteNoOp pins that once a project is
// migrated there is genuinely nothing left to do, so anything reporting an
// update on a re-run is reporting fiction rather than describing the file.
func TestEnsure_SecondRunAfterMigrationIsAByteNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("# Local config\n.ctxloom/*\n"), 0644))

	ensureHarness(t, dir)
	first := readGitignore(t, dir)
	firstNested := readNested(t, dir)
	ensureHarness(t, dir)

	assert.Equal(t, first, readGitignore(t, dir),
		"a migrated .gitignore must come back byte-identical on the next run")
	assert.Equal(t, firstNested, readNested(t, dir),
		"and so must the nested file — it is rewritten wholesale, so a second run cannot accumulate")
}

// TestSupersededBlanketLines_ReadOnly is doctor's gitignore-posture check's
// whole contract: it must be able to ASK whether a blanket rule is present
// without ever writing to the file, unlike every other exported entry point
// in this package (Ensure/EnsureFile/RetireSupersededFile all mutate).
func TestSupersededBlanketLines_ReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	original := "# Local config\n.ctxloom/*\n!.ctxloom/plans/\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0644))

	lines, err := SupersededBlanketLines(path)
	require.NoError(t, err)
	assert.Equal(t, []string{".ctxloom/*"}, lines)

	// The read must not have touched the file at all.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(after), "SupersededBlanketLines must never write")
}

// TestSupersededBlanketLines_MissingFileIsEmptyNotError matches
// RetireSupersededFile's own contract for an absent file: empty, not an
// error, so a caller doing "is there anything to fix" never has to special-
// case "the file doesn't exist yet" as a failure.
func TestSupersededBlanketLines_MissingFileIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	lines, err := SupersededBlanketLines(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Empty(t, lines)
}

// TestSupersededBlanketLines_CleanFileIsEmpty pins the "posture is fine"
// case doctor's check reads as ok: a .gitignore with no blanket line at all.
func TestSupersededBlanketLines_CleanFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte(".ctxloom/cache/\n.ctxloom/sessions/\n"), 0644))

	lines, err := SupersededBlanketLines(path)
	require.NoError(t, err)
	assert.Empty(t, lines)
}

// TestRetireSupersededFile_DegeneratesToEmptyFile pins a legitimate empty
// write: a .gitignore holding NOTHING but the ctxloom-authored header and the
// blanket rule retires down to a genuinely empty file, and that write must
// succeed rather than being caught by iox's empty-over-existing guard
// (replaceFile passes iox.AllowEmpty() for exactly this reason — see
// fs-consolidation plan C3/C4).
func TestRetireSupersededFile_DegeneratesToEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("# ctxloom local files\n.ctxloom/\n"), 0644))

	changed, err := RetireSupersededFile(path)
	require.NoError(t, err)
	assert.True(t, changed)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, string(got), "retiring the file's only content must leave it genuinely empty, not refused")
}

// TestSupersededBlanketLines_AgreesWithRetireSupersededFile pins the review's
// mitigation directly: RetireSupersededFile routes through
// SupersededBlanketLines, so the read-only detector and the mutating
// retirement can never disagree about what counts as superseded. Every
// spelling this package already tests for retirement is checked to have been
// SEEN by the read-only detector first, and to be GONE from it after
// retirement runs.
func TestSupersededBlanketLines_AgreesWithRetireSupersededFile(t *testing.T) {
	for _, blanket := range blanketSpellings {
		t.Run(blanket, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			require.NoError(t, os.WriteFile(path, []byte("# Local config\n"+blanket+"\n"), 0644))

			before, err := SupersededBlanketLines(path)
			require.NoError(t, err)
			assert.NotEmpty(t, before, "the detector must see %q before retirement", blanket)

			changed, err := RetireSupersededFile(path)
			require.NoError(t, err)
			assert.True(t, changed)

			after, err := SupersededBlanketLines(path)
			require.NoError(t, err)
			assert.Empty(t, after, "the detector must see nothing left after retirement removed %q", blanket)
		})
	}
}

// ignoreRules returns content's non-empty, non-comment lines trimmed, so an
// assertion about a RULE cannot be satisfied (or defeated) by the same text
// appearing inside a comment.
func ignoreRules(content string) []string {
	var rules []string
	for line := range strings.SplitSeq(content, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		rules = append(rules, s)
	}
	return rules
}
