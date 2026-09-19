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
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
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

// swtDeadPid is the pid these fixtures stamp into a session's lock file. It
// names no live process on any real kernel.pid_max configuration — but note
// that NOTHING decides liveness from it: the lock's held-ness is the signal,
// and the pid is content for a human reading the file (see
// internal/shared/sessionlock). It is asserted on only where a listing is
// expected to RENDER it.
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
// layout isolation.findEphemeralWorktrees scans — and, when dirty, genuine
// uncommitted content no reap may ever destroy.
//
// It says NOTHING about liveness: that is the owning session's, seeded once
// per harp by swtSeedDeadSession/swtSeedLiveSession, or left unseeded for the
// unprovable case.
func swtAddScratchWorktree(t *testing.T, home, repo, harp, name string, dirty bool) string {
	t.Helper()
	ephemeral := filepath.Join(home, ".ctxloom", "sessions", harp, "ephemeral")
	require.NoError(t, os.MkdirAll(ephemeral, 0o755))
	wtDir := filepath.Join(ephemeral, "ctxloom-wt-"+name)
	swtGit(t, repo, "worktree", "add", "-q", "-b", "wt-"+harp+"-"+name, wtDir)
	if dirty {
		require.NoError(t, os.WriteFile(filepath.Join(wtDir, "in-flight.go"), []byte("// uncommitted\n"), 0o644))
	}
	return wtDir
}

// swtSeedDeadSession makes harp read sessionlock.Dead: a lock file that
// exists and is FREE, the state the kernel leaves once the owning process has
// ended however it ended. The pid written in it is swtDeadPid, so a listing
// has something to render.
func swtSeedDeadSession(t *testing.T, home, harp string) {
	t.Helper()
	lock := filepath.Join(home, ".ctxloom", "sessions", harp+".lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lock), 0o700))
	require.NoError(t, os.WriteFile(lock, []byte(strconv.Itoa(swtDeadPid)+"\n"), 0o600))
}

// swtSeedLiveSession makes harp read sessionlock.Alive: this test process
// holds the lock for the rest of the test, so the probe meets a genuinely
// held lock rather than a simulated one.
func swtSeedLiveSession(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
}

// TestSessionWorktrees_BareListing_ShowsEveryVerdict pins row 4's shape
// directly against runSessionWorktrees rather than through the acceptance
// harness: given a clean/dead-owner tree and a dirty/dead-owner tree, the
// bare listing reports both, removing neither.
func TestSessionWorktrees_BareListing_ShowsEveryVerdict(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	clean := swtAddScratchWorktree(t, home, repo, "amber", "clean", false)
	wip := swtAddScratchWorktree(t, home, repo, "amber", "wip", true)
	swtSeedDeadSession(t, home, "amber")

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
// within one ENDED session, only the clean tree may go, and the uncommitted
// worktree's OWN BYTES must survive, not merely its directory — the project's
// own standing lesson about force-removed worktrees.
//
// Purge is harp-scoped and liveness is per-SESSION, so the live and
// unprovable populations cannot share this harp; they are pinned in the two
// tests below, against the same verb.
func TestSessionWorktrees_ReapYes_RemovesOnlyProvenSafe(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	clean := swtAddScratchWorktree(t, home, repo, "amber", "clean", false)
	wip := swtAddScratchWorktree(t, home, repo, "amber", "wip", true)
	swtSeedDeadSession(t, home, "amber")

	out, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	require.NoError(t, err)

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.True(t, rep.Applied)
	assert.Equal(t, 1, rep.Reaped)
	assert.Equal(t, 1, rep.Spared)
	assert.Equal(t, 0, rep.Skipped)

	assert.NoDirExists(t, clean, "the clean, provably-orphaned worktree must be gone")
	assert.DirExists(t, wip, "uncommitted work is spared IN PLACE")
	body, rerr := os.ReadFile(filepath.Join(wip, "in-flight.go"))
	require.NoError(t, rerr, "the uncommitted file itself must survive, not just its directory")
	assert.Contains(t, string(body), "uncommitted")
}

// TestSessionWorktrees_ReapYes_LiveSessionIsNeverTouched is the half of the
// safety rule the lock exists to enforce: a genuinely CLEAN worktree, which
// every other signal would call reapable, is left strictly alone because its
// owning session still holds its lock. Reaping out from under a running
// session is the incident this design guards against.
func TestSessionWorktrees_ReapYes_LiveSessionIsNeverTouched(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	live := swtAddScratchWorktree(t, home, repo, "amber", "live", false)
	swtSeedLiveSession(t, "amber")

	out, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	require.Error(t, err, "a purge that could remove nothing refuses rather than reporting a clean sweep")

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.Equal(t, 0, rep.Reaped)
	assert.Equal(t, 1, rep.Skipped)
	assert.DirExists(t, live, "a live session's worktree is never touched")
}

// TestSessionWorktrees_ReapYes_UnprovableSessionIsNeverTouched pins the
// conservative default: a session with NO lock file at all cannot be PROVEN
// dead, and "cannot determine" is never permission. A missing lock must never
// read as a free one.
func TestSessionWorktrees_ReapYes_UnprovableSessionIsNeverTouched(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	unknowable := swtAddScratchWorktree(t, home, repo, "amber", "unknowable", false)
	// Deliberately no lock file seeded for "amber".

	out, err := execRootCmd(t, "session", "worktrees", "purge", "amber", "--yes", "--format", "json")
	require.Error(t, err, "nothing was provably safe to remove, so the purge refuses")

	var rep sessionWorktreeReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.Equal(t, 0, rep.Reaped)
	assert.Equal(t, 1, rep.Skipped)
	assert.DirExists(t, unknowable, "an unprovable owner is never touched")
}

// TestSessionWorktrees_ReapWithoutYes_NeverActs is the human override this
// design landed on: the absence of --yes means report-only, unconditionally —
// no TTY-based prompting path exists at all, and the report says so out loud.
func TestSessionWorktrees_ReapWithoutYes_NeverActs(t *testing.T) {
	t.Cleanup(resetSessionWorktreesFlags)
	home := testsupport.Isolate(t)
	repo := swtInitRepo(t)
	clean := swtAddScratchWorktree(t, home, repo, "amber", "clean", false)
	swtSeedDeadSession(t, home, "amber")

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
	swtAddScratchWorktree(t, home, repo, "amber", "wip", true)
	swtSeedDeadSession(t, home, "amber")

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
	swtAddScratchWorktree(t, home, repo, "amber", "clean", false)
	swtAddScratchWorktree(t, home, repo, "amber", "wip", true)
	swtSeedDeadSession(t, home, "amber")

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
	swtAddScratchWorktree(t, home, repo, "amber", "clean", false)
	swtSeedDeadSession(t, home, "amber")

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
	swtAddScratchWorktree(t, home, repo, "amber", "clean", false)
	other := swtAddScratchWorktree(t, home, repo, "brisk", "clean", false)
	swtSeedDeadSession(t, home, "amber")
	swtSeedDeadSession(t, home, "brisk")

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
