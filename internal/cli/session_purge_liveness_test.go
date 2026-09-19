package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The file destroyers consult the session lock, and the lock only ever
// REFUSES. A free lock proves the owner dead and purging proceeds; a held
// lock, or no lock at all, refuses until the human passes --even-if-live —
// the one irreversible mistake takes a second, deliberate keystroke.
//
// Every test here that expects a refusal asserts on DISK that the file is
// still there: the refusal text alone would also be printed by a destroyer
// that refused after unlinking.

// fileDestroyer is one `session` leaf that reaches operations.PurgeSession,
// with the file it would destroy for harp. All three share the gate, so all
// three are driven — a leaf left out is the side door to the same data.
type fileDestroyer struct {
	cmd    *cobra.Command
	argv   []string
	target string
}

func fileDestroyers(t *testing.T, harp string) []fileDestroyer {
	t.Helper()
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	return []fileDestroyer{
		{sessionPurgeCmd, []string{"session", "purge", harp}, transcript},
		{sessionTranscriptPurgeCmd, []string{"session", "transcript", "purge", harp}, transcript},
		{sessionArtifactsPurgeCmd, []string{"session", "artifacts", "purge", harp}, essence},
	}
}

// removeSessionLock turns harp into a session from before the lock existed:
// no lock file at all, which the probe reads as Indeterminate.
func removeSessionLock(t *testing.T, harp string) {
	t.Helper()
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(lock))
}

// TestSessionPurge_EvenIfLiveIsOptInOnEveryFileDestroyer pins the flag's
// name and its default on every leaf that can reach the transcript or the
// essence. The default is the load-bearing half: an escape that is on unless
// switched off is not an escape.
func TestSessionPurge_EvenIfLiveIsOptInOnEveryFileDestroyer(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	for _, d := range fileDestroyers(t, harp) {
		fl := d.cmd.Flags().Lookup("even-if-live")
		require.NotNil(t, fl, "%s must carry --even-if-live", d.cmd.CommandPath())
		assert.Equal(t, "false", fl.DefValue, "%s: --even-if-live must be opt-in", d.cmd.CommandPath())
	}
}

// TestSessionPurge_HeldLockRefusesEveryFileDestroyer: the session ended in
// the index and was then RESUMED, so its lock is held again while ended_at
// still reads as set. The index cannot see this; the lock can.
//
// MUTATION: invert the lock sense (a held lock permits) → red on every leaf.
// MUTATION: make --even-if-live the default → red on every leaf.
func TestSessionPurge_HeldLockRefusesEveryFileDestroyer(t *testing.T) {
	for _, d := range fileDestroyers(t, seedHarpFor(t)) {
		t.Run(d.cmd.CommandPath(), func(t *testing.T) {
			t.Cleanup(func() { resetSessionPurgeFlags(t) })
			harp := d.argv[len(d.argv)-1]
			swtSeedLiveSession(t, harp)

			_, stderr, err := execRootCmdBoth(t, append(d.argv, "--yes")...)
			require.Error(t, err, "a held lock must refuse")
			assert.True(t, onDisk(t, d.target), "a refusal destroys nothing")
			assert.Contains(t, stderr, "alive")
			assert.Contains(t, stderr, "--even-if-live", "the refusal names the deliberate escape")
		})
	}
}

// seedHarpFor is seedEndedSession under a fresh isolated project, for the
// table-driven tests above that share one harp across every destroyer.
func seedHarpFor(t *testing.T) string {
	t.Helper()
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	return harp
}

// TestSessionPurge_NoLockRefusesAndNamesTheEscape: a session from before the
// lock existed has no lock file. "Cannot determine" is never permission — but
// it is precisely the population people want to purge, so the refusal names
// the exact command that does it deliberately, and says so in the report-only
// run too, before anyone has typed --yes.
//
// MUTATION: treat Indeterminate as permission → red.
func TestSessionPurge_NoLockRefusesAndNamesTheEscape(t *testing.T) {
	harp := seedHarpFor(t)
	transcript := seedTranscript(t, harp)
	seedEssence(t, harp)
	removeSessionLock(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	_, stderr, err := execRootCmdBoth(t, "session", "purge", harp, "--yes")
	require.Error(t, err, "no lock must refuse")
	assert.True(t, onDisk(t, transcript), "a refusal destroys nothing")
	assert.Contains(t, stderr, "no session lock")
	assert.Contains(t, stderr, "ctxloom session purge "+harp+" --yes --even-if-live",
		"the refusal must name the exact command that applies it anyway")

	resetSessionPurgeFlags(t)
	_, stderr, err = execRootCmdBoth(t, "session", "purge", harp)
	require.Error(t, err, "the report-only run refuses too, so --yes is never the first time the human hears of it")
	assert.True(t, onDisk(t, transcript))
	assert.Contains(t, stderr, "--even-if-live")
}

// TestSessionPurge_EvenIfLiveDestroysAnyway is the escape's apply side, for
// both verdicts the lock cannot turn into permission.
func TestSessionPurge_EvenIfLiveDestroysAnyway(t *testing.T) {
	t.Run("no lock", func(t *testing.T) {
		harp := seedHarpFor(t)
		transcript := seedTranscript(t, harp)
		essence := seedEssence(t, harp)
		removeSessionLock(t, harp)
		t.Cleanup(func() { resetSessionPurgeFlags(t) })

		_, err := execRootCmd(t, "session", "purge", harp, "--yes", "--even-if-live")
		require.NoError(t, err)
		assert.False(t, onDisk(t, transcript))
		assert.False(t, onDisk(t, essence))
	})
	t.Run("held lock, transcript leaf", func(t *testing.T) {
		harp := seedHarpFor(t)
		transcript := seedTranscript(t, harp)
		essence := seedEssence(t, harp)
		swtSeedLiveSession(t, harp)
		t.Cleanup(func() { resetSessionPurgeFlags(t) })

		_, err := execRootCmd(t, "session", "transcript", "purge", harp, "--yes", "--even-if-live")
		require.NoError(t, err)
		assert.False(t, onDisk(t, transcript))
		assert.True(t, onDisk(t, essence), "the leaf still reaches only its own population")
	})
}

// TestSessionPurge_DeadOwnerSweepReapsItsCleanScratchWorktree guards the
// sweep against probing the lock from under its own hold. The file half
// takes the lock while it destroys; if the worktree half then probed under
// that hold, flock would report the sweep's OWN lock as a live owner and
// every worktree of a provably dead session would be skipped — a failure
// that looks exactly like correct conservatism. So: a dead owner, one clean
// scratch worktree, one sweep, and the worktree must actually go.
func TestSessionPurge_DeadOwnerSweepReapsItsCleanScratchWorktree(t *testing.T) {
	home := testsupport.Isolate(t)
	_, harp := seedEndedSession(t, t.TempDir(), "claude-code")
	transcript := seedTranscript(t, harp)
	seedEssence(t, harp)
	repo := swtInitRepo(t)
	wt := swtAddScratchWorktree(t, home, repo, harp, "clean", false)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	out, err := execRootCmd(t, "session", "purge", harp, "--yes", "--format", "json")
	require.NoError(t, err)

	var rep sessionSweepReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep), "output must be clean JSON: %s", out)
	assert.False(t, onDisk(t, transcript), "the file half went")
	assert.Equal(t, 1, rep.Worktrees.Reaped, "the worktree half must see the same dead owner the file half did: %s", out)
	assert.NoDirExists(t, wt)
}
