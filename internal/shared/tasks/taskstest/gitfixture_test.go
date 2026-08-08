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
// env/dir plumbing and so cannot call the canonical body at all, and J13's
// close-out steps (both the acceptance scenarios and `session worktrees`'
// own CLI-level unit tests).
//
// J13 is the exception that is about the worktrees themselves rather than
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
	filepath.Join("tests", "acceptance", "steps_j13_closeout.go"):                   true,
	filepath.Join("internal", "cli", "session_worktrees_test.go"):                   true,
}

// TestRealGitWorktreeFixture_CanonicalityClaimHolds enforces the claim
// RealGitWorktreeFixture's doc makes about itself. A "canonical" claim nobody
// checks stops the next author from checking: they read the doc, believe the
// count, and add the copy it says not to add.
func TestRealGitWorktreeFixture_CanonicalityClaimHolds(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The distinguishing shape: a linked worktree created on a fresh
		// branch, which is what the fixture exists to build.
		if !strings.Contains(string(body), `"worktree", "add", "-q", "-b"`) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !sanctionedWorktreeFixtureFiles[rel] {
			offenders = append(offenders, rel)
		}
		return nil
	}))
	assert.Empty(t, offenders,
		"a linked-worktree fixture body exists outside the sanctioned files; use RealGitWorktreeFixture, or amend its doc and this list together")
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
