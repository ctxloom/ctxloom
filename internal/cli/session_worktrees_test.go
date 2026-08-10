package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetSessionWorktreesFlags restores the package-level cobra flag vars
// backing `session worktrees` to their zero values, AND clidiag's process-
// wide structured-diagnostics flag to off. Two independent leaks, one
// helper, because every test in this file trips both the same way:
//
//   - pflag only calls Set() on flags actually present in a given argv, so a
//     value a PRIOR test's execRootCmd left on (e.g. --yes=true) survives
//     into a later invocation that never mentions the flag at all — the same
//     reason doctor_cmd_test.go resets doctorDepsOnlyFlag.
//   - root.go's PersistentPreRun calls clidiag.SetStructured(true) for every
//     --format json/yaml/toml invocation and never resets it — root_test.go,
//     format_test.go, group_node_test.go and llm_resolve_test.go all reset it
//     in t.Cleanup for the same reason. Every test below runs with
//     --format json, so left unreset it flips clidiag's stderr shape for
//     every test that runs after it in this package's binary, INCLUDING
//     ones in other files (startup_helpers_test.go's warning-prefix
//     assertions, discovered exactly this way).
//
// Every test in this file that runs "session worktrees" registers this in
// t.Cleanup, regardless of which flags/format it itself sets, so no test can
// leak state into whichever test runs after it.
func resetSessionWorktreesFlags() {
	sessionWorktreesPurgeYes = false
	clidiag.SetStructured(false)
}

// swtDeadPid mirrors isolation's own deadPid / tests/acceptance's j001300DeadPid:
// a pid guaranteed not to name a live process on any real Linux
// kernel.pid_max configuration, so "this owner is confirmed dead" is
// deterministic with no fork/kill/race.
const swtDeadPid = 999999999

// swtGit runs a git command in dir with a stable, hermetic identity —
// mirroring isolation's own worktree_integration_test.go gitCmd, duplicated
// here rather than exported across a package boundary for a two-line helper.
func swtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
		"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// swtInitRepo creates a fresh real git repo with one commit, so `git worktree
// add -b` has a HEAD to branch from.
func swtInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	swtGit(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644))
	swtGit(t, dir, "add", "README.md")
	swtGit(t, dir, "commit", "-q", "-m", "seed")
	return dir
}

// swtAddScratchWorktree creates a REAL linked worktree under
// <home>/.ctxloom/sessions/<harp>/ephemeral/ctxloom-wt-<name>, exactly the
// layout isolation.findEphemeralWorktrees scans — with a sibling ".owner.pid"
// marker (ownerPid == 0 means "write no marker at all") and, when dirty,
// genuine uncommitted content no reap may ever destroy.
func swtAddScratchWorktree(t *testing.T, home, repo, harp, name string, ownerPid int, dirty bool) string {
	t.Helper()
	ephemeral := filepath.Join(home, ".ctxloom", "sessions", harp, "ephemeral")
	require.NoError(t, os.MkdirAll(ephemeral, 0o755))
	wtDir := filepath.Join(ephemeral, "ctxloom-wt-"+name)
	swtGit(t, repo, "worktree", "add", "-q", "-b", "wt-"+harp+"-"+name, wtDir)
	if ownerPid != 0 {
		require.NoError(t, os.WriteFile(wtDir+".owner.pid", []byte(strconv.Itoa(ownerPid)+"\n"), 0o600))
	}
	if dirty {
		require.NoError(t, os.WriteFile(filepath.Join(wtDir, "in-flight.go"), []byte("// uncommitted\n"), 0o644))
	}
	return wtDir
}

// TestSessionWorktrees_BareListing_ShowsEveryVerdict pins row 4's shape
// directly against runSessionWorktrees rather than through the acceptance
// harness: given a clean/dead-owner tree and a dirty/dead-owner tree, the
// bare listing reports both, removing neither.
func TestSessionWorktrees_BareListing_ShowsEveryVerdict(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	clean := swtAddScratchWorktree(t, home, repo, "amber", "clean", swtDeadPid, false)
	wip := swtAddScratchWorktree(t, home, repo, "amber", "wip", swtDeadPid, true)

	out, err := execRootCmd(t, "session", "worktrees", "--format", "json")
	require.NoError(t, err)

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep), "output must be clean JSON: %s", out)

	require.Len(t, rep.Worktrees, 2)
	byName := map[string]sessionWorktreeRow{}
	for _, r := range rep.Worktrees {
		byName[r.Worktree] = r
	}
	require.Contains(t, byName, "ctxloom-wt-clean")
	require.Contains(t, byName, "ctxloom-wt-wip")
	assert.Equal(t, "reapable", byName["ctxloom-wt-clean"].Verdict)
	assert.Equal(t, "spared", byName["ctxloom-wt-wip"].Verdict)
	assert.NotEmpty(t, byName["ctxloom-wt-wip"].Reason, "a spared worktree's reason must never be empty")
	assert.Equal(t, swtDeadPid, byName["ctxloom-wt-clean"].OwnerPID)
	assert.False(t, rep.Applied, "a bare listing must never apply anything")

	// Read-only: nothing on disk moved.
	assert.DirExists(t, clean)
	assert.DirExists(t, wip)
}

// TestSessionWorktrees_ReapYes_RemovesOnlyProvenSafe is row 5's direct pin:
// four populations, one invocation, and only the clean/dead-owner tree may
// go. The uncommitted worktree's OWN BYTES must survive, not merely its
// directory — the project's own standing lesson about force-removed
// worktrees.
func TestSessionWorktrees_ReapYes_RemovesOnlyProvenSafe(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	clean := swtAddScratchWorktree(t, home, repo, "amber", "clean", swtDeadPid, false)
	wip := swtAddScratchWorktree(t, home, repo, "amber", "wip", swtDeadPid, true)
	unknowable := swtAddScratchWorktree(t, home, repo, "amber", "unknowable", 0, false)
	live := swtAddScratchWorktree(t, home, repo, "amber", "live", os.Getpid(), false)

	out, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	require.NoError(t, err)

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.True(t, rep.Applied)
	assert.Equal(t, 1, rep.Reaped)
	assert.Equal(t, 1, rep.Spared)
	assert.Equal(t, 2, rep.Skipped)

	assert.NoDirExists(t, clean, "the clean, provably-orphaned worktree must be gone")
	assert.DirExists(t, wip, "uncommitted work is spared IN PLACE")
	body, rerr := os.ReadFile(filepath.Join(wip, "in-flight.go"))
	require.NoError(t, rerr, "the uncommitted file itself must survive, not just its directory")
	assert.Contains(t, string(body), "uncommitted")
	assert.DirExists(t, unknowable, "an indeterminate owner is never touched")
	assert.DirExists(t, live, "a live owner is never touched")
}

// TestSessionWorktrees_ReapWithoutYes_NeverActs is the human override this
// design landed on: the absence of --yes means report-only, unconditionally —
// no TTY-based prompting path exists at all, and the report says so out loud.
func TestSessionWorktrees_ReapWithoutYes_NeverActs(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	clean := swtAddScratchWorktree(t, home, repo, "amber", "clean", swtDeadPid, false)

	out, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--format", "json")
	require.NoError(t, err)

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.False(t, rep.Applied, "purge without --yes must never apply")
	assert.DirExists(t, clean, "nothing may be removed without --yes")
}

// TestSessionWorktrees_ReapYes_ChangedNothing_Refuses pins the human's exit-
// code override: an action verb (--reap --yes) that removed nothing exits 2,
// even though every candidate was legitimately spared/skipped rather than
// erroring.
func TestSessionWorktrees_ReapYes_ChangedNothing_Refuses(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	swtAddScratchWorktree(t, home, repo, "amber", "wip", swtDeadPid, true)

	_, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	require.Error(t, err, "a purge that removed nothing must refuse, not exit 0")
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeRefused, exitErr.Code)
}

// TestSessionWorktrees_ReapYes_SomethingChanged_Succeeds is the contrasting
// case: a purge that removes at least one candidate exits 0 even though other
// candidates were spared — sparing is a delivered effect, not a refusal, as
// long as SOMETHING moved.
func TestSessionWorktrees_ReapYes_SomethingChanged_Succeeds(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	swtAddScratchWorktree(t, home, repo, "amber", "clean", swtDeadPid, false)
	swtAddScratchWorktree(t, home, repo, "amber", "wip", swtDeadPid, true)

	_, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	assert.NoError(t, err)
}

// TestSessionWorktrees_ForeignWorktreeNeverListed is row 6's direct pin: a
// long-lived worktree living outside ~/.ctxloom/sessions is invisible to this
// verb by construction, never merely by an added filter.
func TestSessionWorktrees_ForeignWorktreeNeverListed(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	swtAddScratchWorktree(t, home, repo, "amber", "clean", swtDeadPid, false)

	foreignDir := filepath.Join(home, "workspace", "worktrees", "proj--stale-feature")
	require.NoError(t, os.MkdirAll(filepath.Dir(foreignDir), 0o755))
	swtGit(t, repo, "worktree", "add", "-q", "-b", "stale-feature", foreignDir)

	out, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	require.NoError(t, err)
	assert.NotContains(t, out, foreignDir, "a worktree ctxloom did not create must never appear in this report")
	assert.DirExists(t, foreignDir, "and must never be touched")
}

// TestSessionWorktrees_HarpFilter_ScopesToOneHarp proves the positional harp
// restricts the SCAN rather than merely filtering a full listing after the
// fact — a second harp's own scratch worktree must not even be classified.
func TestSessionWorktrees_HarpFilter_ScopesToOneHarp(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	swtAddScratchWorktree(t, home, repo, "amber", "clean", swtDeadPid, false)
	other := swtAddScratchWorktree(t, home, repo, "brisk", "clean", swtDeadPid, false)

	out, err := execRootCmd(t, "session", "worktrees", "list", "amber", "--format", "json")
	require.NoError(t, err)

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	require.Len(t, rep.Worktrees, 1)
	assert.Equal(t, "amber", rep.Worktrees[0].Harp)
	assert.DirExists(t, other, "the other harp's worktree is untouched by a scoped listing")
}

// TestSessionWorktrees_HarpNotFound_IsOrdinaryError pins the design's
// "the harp named has no directory" row: an ordinary error (exit 1), not a
// refusal.
func TestSessionWorktrees_HarpNotFound_IsOrdinaryError(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	testsupport.Isolate(t)
	_, err := execRootCmd(t, "session", "worktrees", "list", "no-such-harp-ever")
	require.Error(t, err)
	var exitErr *ExitError
	assert.False(t, errors.As(err, &exitErr), "a not-found harp is an ordinary error, not a coded refusal")
}
