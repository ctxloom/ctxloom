//go:build unix

package isolation

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerRunnerWait_DescendantHoldingThePipesDoesNotWedgeIt REFUTES a
// finding, which claimed containerRunner.Wait "keeps the pipe-wedge hazard"
// its siblings (nonInteractiveWaitDelay, hostRunnerWaitDelay, attachWaitDelay)
// fixed with cmd.WaitDelay, because it is a bare cmd.Wait() with none set.
//
// The hazard is real for THOSE cmds and absent for this one, and the
// difference is the I/O shape, not the delay. os/exec's Wait blocks on a held
// pipe only through the copy goroutines it starts when Stdout/Stderr is an
// io.Writer (the siblings' stderr rings) — it is those goroutines, reading
// until EOF, that a surviving descendant holds open. containerRunner hands
// StdoutPipe/StderrPipe to go-plugin instead: no copy goroutine exists, Wait
// returns the moment the `run` CLI is reaped, and WaitDelay would bound
// nothing. Setting one here would be a comment lying about a hazard the
// shape cannot have.
//
// Measured, not argued: a descendant holds BOTH pipes open long past the
// parent's exit, and Wait must still return promptly with no WaitDelay set.
func TestContainerRunnerWait_DescendantHoldingThePipesDoesNotWedgeIt(t *testing.T) {
	rt := scriptRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "sh", available: true},
		// The backgrounded sleep inherits the run process's stdout AND stderr
		// write ends and keeps both open for 30s after `sh` itself exits —
		// the exact surviving-descendant shape the finding named.
		args: []string{"-c", "sleep 30 & echo $!; exit 0"},
	}
	r, err := newContainerRunner(rt, RunSpec{Name: "ctxloom-iso-held-pipes"}, "", "", nil)
	require.NoError(t, err)
	require.Zero(t, r.cmd.WaitDelay, "sanity: this pins the UNBOUNDED shape — no WaitDelay is set")
	require.NoError(t, r.Start(context.Background()))

	// The descendant's pid, so this test never leaves it behind.
	line, err := bufio.NewReader(r.stdout).ReadString('\n')
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	require.NoError(t, err)
	holder, err := os.FindProcess(pid)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Kill() })

	waited := make(chan error, 1)
	go func() { waited <- r.Wait(context.Background()) }()

	select {
	case werr := <-waited:
		assert.NoError(t, werr, "the run CLI exited 0; a held pipe is not an exit failure")
	case <-time.After(2 * time.Second):
		t.Fatal("Wait wedged on a pipe a surviving descendant holds open — the finding stands and WaitDelay is needed")
	}
	assert.NoError(t, holder.Signal(syscall.Signal(0)), "sanity: the descendant is still alive, so the pipes really were held while Wait returned")
}
