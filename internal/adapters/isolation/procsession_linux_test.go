package isolation

import (
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// init confines killSession to THIS test binary's own process tree, for every
// test in the package and before any of them runs.
//
// killSession's only guard against signalling a stranger is one session-id
// comparison, and mutation testing negates exactly that comparison: the mutant
// SIGKILLs every process it can signal EXCEPT the target's session. Against
// the real /proc that is the whole machine — in CI it took down gremlins, just
// and the job itself (exit 137). Narrowing the sweep here means any mutant can
// reach only processes this binary spawned.
//
// The subreaper is what keeps "this binary's tree" honest: an orphan (a
// grandchild whose runner was hard-killed — the exact case
// TestKillSession_ReapsOrphanedGrandchild drives) is reparented to the nearest
// subreaper ancestor instead of init, so it stays a descendant and in scope.
func init() {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		panic("isolation tests: become child subreaper: " + err.Error())
	}
	sessionSweepPids = ownTreePids
}

// ownTreePids is procPids narrowed to strict descendants of this process.
func ownTreePids() []int {
	self := os.Getpid()
	all := procPids()
	parent := make(map[int]int, len(all))
	for _, pid := range all {
		parent[pid] = procStatInt(pid, statPPID)
	}
	var mine []int
	for _, pid := range all {
		// The step bound breaks a cycle a pid reused mid-snapshot could form.
		for p, n := parent[pid], 0; p > 0 && n < len(all); p, n = parent[p], n+1 {
			if p == self {
				mine = append(mine, pid)
				break
			}
		}
	}
	return mine
}

// TestIsolateRunner_ForeignRunnerDiesWithItsHost pins that PR_SET_PDEATHSIG
// (setRunnerPdeathsig) takes down a runner that carries no watcher of its
// own — a foreign `sleep`. Linux-only because nothing else can: darwin/BSD
// have no kernel parent-death attribute, and their in-child watch
// (parentwatch) only runs inside our own binary; that path is
// TestIsolateRunner_RunnerDiesWithItsHost.
func TestIsolateRunner_ForeignRunnerDiesWithItsHost(t *testing.T) {
	assertRunnerDiesWithItsHost(t, "", "sleep", "100")
}

// TestSessionSweepScope_ExcludesEverythingOutsideTheTestBinary pins the
// safety net above: if it failed open, a killSession mutant would again reach
// the go-test driver, the mutation tool and everything else on the machine.
func TestSessionSweepScope_ExcludesEverythingOutsideTheTestBinary(t *testing.T) {
	child := exec.Command("sleep", "100")
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })

	scope := sessionSweepPids()
	require.Contains(t, scope, child.Process.Pid, "a child this binary spawned must be in scope")
	require.NotContains(t, scope, os.Getpid(), "the test binary itself must be out of scope")
	require.NotContains(t, scope, os.Getppid(), "the process that launched the test binary must be out of scope")
	require.NotContains(t, scope, 1, "init must be out of scope")
}

// TestKillSession_SparesAProcessInAnotherSession: the sweep must reap the
// target session and nothing else. The decoy lives in a session of its own,
// so a sweep that ignored or inverted the session filter would take it.
func TestKillSession_SparesAProcessInAnotherSession(t *testing.T) {
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "100")
		isolateRunner(cmd)
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	// Decoy FIRST: pids are handed out ascending, so the sweep meets the decoy
	// before the target, and a sweep that stops at its first non-match never
	// reaches the target.
	decoy := start()
	target := start()

	require.True(t, slices.Contains(sessionSweepPids(), decoy.Process.Pid),
		"sanity: the decoy is in the sweep's view, so sparing it is the filter's decision")

	killSession(target.Process.Pid)
	require.Eventually(t, func() bool { return !processAlive(target.Process.Pid) }, 2*time.Second, 10*time.Millisecond,
		"killSession must reap its target session")
	require.True(t, processAlive(decoy.Process.Pid), "killSession must spare a process in another session")
}
