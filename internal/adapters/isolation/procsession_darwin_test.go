//go:build darwin

package isolation

import (
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// init confines killSession to THIS test binary's own process tree, for every
// test in the package and before any of them runs — the darwin counterpart of
// procsession_linux_test.go's init, for the same reason: a mutant that negates
// sessionGroups' session comparison would otherwise SIGKILL every group it can
// signal. darwin has no subreaper, so the scope is groupSafeScope, which keeps
// an orphaned session member in view by its session id instead.
func init() {
	sessionSweepProcs = ownTreeProcs
}

// ownTreeProcs is allProcs narrowed to groupSafeScope of this process.
func ownTreeProcs() []unix.KinfoProc {
	all := allProcs()
	rows := make([]procRow, len(all))
	for i := range all {
		p := &all[i]
		pid := int(p.Proc.P_pid)
		sid, err := unix.Getsid(pid)
		if err != nil {
			sid = -1
		}
		rows[i] = procRow{pid: pid, ppid: int(p.Eproc.Ppid), pgid: int(p.Eproc.Pgid), sid: sid}
	}
	scope := groupSafeScope(rows, os.Getpid())
	return slices.DeleteFunc(all, func(p unix.KinfoProc) bool { return !scope[int(p.Proc.P_pid)] })
}

// TestSessionSweepScope_ExcludesEverythingOutsideTheTestTree pins the safety
// net above against the real kern.proc.all: if it failed open, a killSession
// mutant would again reach go test, the mutation tool and the rest of the
// machine.
func TestSessionSweepScope_ExcludesEverythingOutsideTheTestTree(t *testing.T) {
	start := func(isolate bool) int {
		cmd := exec.Command("sleep", "100")
		if isolate {
			isolateRunner(cmd)
		}
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid
	}
	isolated, plain := start(true), start(false)

	var scope []int
	for _, p := range sessionSweepProcs() {
		scope = append(scope, int(p.Proc.P_pid))
	}
	require.Contains(t, scope, isolated, "a child leading its own session must be in scope")
	require.NotContains(t, scope, plain,
		"a child still in this binary's own group must be out of scope: signalling its group reaches go test")
	require.NotContains(t, scope, os.Getpid(), "the test binary itself must be out of scope")
	require.NotContains(t, scope, os.Getppid(), "the process that launched the test binary must be out of scope")
	require.NotContains(t, scope, 1, "launchd must be out of scope")
}
