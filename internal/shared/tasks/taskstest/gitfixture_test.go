package taskstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sanctionedWorktreeFixtureFiles are the only files allowed to build a real
// linked worktree by hand, and they must match RealGitWorktreeFixture's doc:
// this package (the canonical body), internal/config's frozen acceptance gate
// (byte-for-byte unmodifiable, so it keeps its own copy), the acceptance
// suite's TestEnvironment, which must route git through its own isolated
// env/dir plumbing and so cannot call the canonical body at all, and J001300's
// close-out steps (both the acceptance scenarios and `session worktrees`'
// own CLI-level unit tests).
//
// J001300 is the exception that is about the worktrees themselves rather than
// about something that happens to need one. Its scenarios triage a POPULATION
// in deliberately varied states — merged and clean, unmerged, dirty with
// uncommitted work, owned by another process — because the whole point of
// `session worktrees --reap` is that it must reap only the first and report
// the rest. RealGitWorktreeFixture builds exactly one main/linked pair in one
// state, which is the right shape for a caller that needs A worktree and the
// wrong shape for a caller whose subject is the difference between several.
// internal/cli/session_worktrees_test.go needs the identical population (the
// CLI's own fast, non-acceptance-harness coverage of the same taxonomy) for
// the identical reason.
var sanctionedWorktreeFixtureFiles = map[string]bool{
	filepath.Join("internal", "shared", "tasks", "taskstest", "gitfixture.go"):      true,
	filepath.Join("internal", "shared", "tasks", "taskstest", "gitfixture_test.go"): true,
	filepath.Join("internal", "config", "worktree_signpost_test.go"):                true,
	filepath.Join("tests", "integration", "testenv", "environment.go"):              true,
	filepath.Join("tests", "acceptance", "steps_j001300_closeout.go"):               true,
	filepath.Join("internal", "cli", "session_worktrees_test.go"):                   true,
}

// worktreeFixtureMarker is the distinguishing shape of a hand-built fixture: a
// linked worktree created on a fresh branch, which is what
// RealGitWorktreeFixture exists to build.
const worktreeFixtureMarker = `"worktree", "add", "-q", "-b"`

// TestRealGitWorktreeFixture_CanonicalityClaimHolds enforces the claim
// RealGitWorktreeFixture's doc makes about itself. A "canonical" claim nobody
// checks stops the next author from checking: they read the doc, believe the
// count, and add the copy it says not to add.
func TestRealGitWorktreeFixture_CanonicalityClaimHolds(t *testing.T) {
	root := repoRoot(t)
	bodies, err := worktreeFixtureBodiesUnder(root)
	require.NoError(t, err)
	var offenders []string
	for _, rel := range bodies {
		if !sanctionedWorktreeFixtureFiles[rel] {
			offenders = append(offenders, rel)
		}
	}
	assert.Empty(t, offenders,
		"a linked-worktree fixture body exists outside the sanctioned files; use RealGitWorktreeFixture, or amend its doc and this list together")
}

// worktreeFixtureBodiesUnder returns, relative to root, every .go file holding
// worktreeFixtureMarker.
//
// It descends the working tree rather than asking git for tracked paths on
// purpose: an unstaged new copy is exactly the drift the canonicality claim
// must catch, and `git ls-files` would let it through. That means the walk
// must exclude nested LINKED WORKTREES itself, because a checkout that hosts
// agent worktrees (.claude/worktrees/agent-*) carries a full second copy of
// every sanctioned file at a path no sanctioned entry can match. Those copies
// are machine debris, not drift; flagging them makes the claim fail for
// everyone whose checkout happens to be the busy one.
func worktreeFixtureBodiesUnder(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			// Never skip root: root itself is routinely a linked worktree
			// (every agent worktree is), and skipping it would scan nothing
			// and report a vacuous PASS.
			_ = isLinkedWorktreeRoot // RED-FIRST: pre-fix behaviour, no skip.
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), worktreeFixtureMarker) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found = append(found, rel)
		return nil
	})
	return found, err
}

// isLinkedWorktreeRoot reports whether dir is the root of a git linked
// worktree. git marks those with a .git FILE holding a gitdir pointer, where a
// primary checkout gets a .git DIRECTORY — the same discriminator
// TestRealGitWorktreeFixture_BuildsARealLinkedWorktree pins. It beats matching
// on a path convention like .claude/worktrees/, which only covers the
// worktrees this tool happens to create today.
func isLinkedWorktreeRoot(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && info.Mode().IsRegular()
}

// TestWorktreeFixtureBodiesUnder_SkipsNestedLinkedWorktrees is the regression:
// the scan used to walk into nested linked worktrees and flag their copies of
// the sanctioned files, so the canonicality claim failed on machine debris
// (any checkout hosting agent worktrees) instead of on real drift.
func TestWorktreeFixtureBodiesUnder_SkipsNestedLinkedWorktrees(t *testing.T) {
	root := t.TempDir()

	// The nested worktree's own copy of a sanctioned file. Its path relative
	// to root carries the .claude/worktrees/... prefix, so no sanctioned entry
	// could ever match it.
	nested := filepath.Join(root, ".claude", "worktrees", "agent-deadbeef")
	writeFixtureBody(t, filepath.Join(nested,
		"internal", "shared", "tasks", "taskstest", "gitfixture.go"))
	// git's marker for a linked worktree root: a .git FILE, not a directory.
	require.NoError(t, os.WriteFile(filepath.Join(nested, ".git"),
		[]byte("gitdir: /somewhere/.git/worktrees/agent-deadbeef\n"), 0o644))

	bodies, err := worktreeFixtureBodiesUnder(root)
	require.NoError(t, err)
	assert.Empty(t, bodies,
		"a nested linked worktree's copies are machine debris, not a new fixture body")
}

// TestWorktreeFixtureBodiesUnder_StillCatchesStrayCopies is the other
// direction, and the whole point of the scan: skipping nested worktrees must
// not turn into skipping anything. A stray copy at an ordinary path stays
// visible.
func TestWorktreeFixtureBodiesUnder_StillCatchesStrayCopies(t *testing.T) {
	root := t.TempDir()
	stray := filepath.Join("internal", "elsewhere", "helpers_test.go")
	writeFixtureBody(t, filepath.Join(root, stray))

	// A sibling linked worktree, to prove the stray is found by walking PAST a
	// skip rather than because nothing was skipped at all.
	nested := filepath.Join(root, ".claude", "worktrees", "agent-deadbeef")
	writeFixtureBody(t, filepath.Join(nested, "internal", "elsewhere", "helpers_test.go"))
	require.NoError(t, os.WriteFile(filepath.Join(nested, ".git"),
		[]byte("gitdir: /somewhere/.git/worktrees/agent-deadbeef\n"), 0o644))

	// Deliberately Contains, not Equal: this test must hold both before and
	// after the nested-worktree skip lands, so that the only thing that can
	// turn it red is a skip that swallows ordinary paths too.
	bodies, err := worktreeFixtureBodiesUnder(root)
	require.NoError(t, err)
	assert.Contains(t, bodies, stray,
		"a stray fixture body at an ordinary path must still be reported")
}

// writeFixtureBody plants a .go file carrying worktreeFixtureMarker at path,
// creating parents.
func writeFixtureBody(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	body := "package planted\n\nfunc f() { runGit(t, dir, " + worktreeFixtureMarker + ", b, d) }\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestRealGitWorktreeFixture_BuildsARealLinkedWorktree pins what the fixture
// hands back: two distinct, absolute, symlink-resolved roots, the second a
// linked worktree of the first.
func TestRealGitWorktreeFixture_BuildsARealLinkedWorktree(t *testing.T) {
	main, linked := RealGitWorktreeFixture(t)

	assert.True(t, filepath.IsAbs(main), "main root %q is not absolute", main)
	assert.True(t, filepath.IsAbs(linked), "linked root %q is not absolute", linked)
	assert.NotEqual(t, main, linked)

	// A primary checkout has a .git DIRECTORY; a linked worktree has a .git
	// FILE pointing into the primary's gitdir.
	mainGit, err := os.Stat(filepath.Join(main, ".git"))
	require.NoError(t, err)
	assert.True(t, mainGit.IsDir(), "main checkout's .git must be a directory")

	linkedGit, err := os.Stat(filepath.Join(linked, ".git"))
	require.NoError(t, err)
	assert.False(t, linkedGit.IsDir(), "linked worktree's .git must be a gitdir pointer file")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "walked to the filesystem root without finding go.mod")
		dir = parent
	}
}

// TestRequireGit_DefaultsToRequiring pins the load-bearing half of the git
// gate: the DEFAULT. A missing git must fail, not skip, because a skip makes
// all 16 RealGitWorktreeFixture call sites — the only coverage of the
// linked-worktree redirect logic — evaporate while the suite still reports
// PASS. The escape hatch exists; it must stay opt-in, since a default that
// skips is exactly the state this row objected to.
func TestRequireGit_DefaultsToRequiring(t *testing.T) {
	assert.False(t, allowMissingGit,
		"a missing git must be a failure unless %s=1 is set explicitly", EnvAllowMissingGit)
	assert.Contains(t, EnvKeys, EnvAllowMissingGit,
		"Isolate must clear the knob, and the production-read sweep must find it declared here")
}
