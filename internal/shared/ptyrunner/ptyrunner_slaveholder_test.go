//go:build !windows

package ptyrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slaveHolderReturnBound is how long RunInteractive may take to return once
// its child has exited while another process still holds the pty's slave:
// the drain's grace and the forced close, with room for a loaded machine.
const slaveHolderReturnBound = ptyDrainGrace + 5*time.Second

// TestRunInteractive_ReturnsWhileAnOrphanStillHoldsTheSlave forces the case
// the forced master close exists for: the child exits, but a process it
// started — which ignores the hangup the kernel sends when the session leader
// dies, as an MCP server or a tool a crashed engine leaves behind may —
// still holds the slave open, so no end-of-file ever reaches the master.
// RunInteractive must still return the child's exit; the close has to wake
// the output copier's read rather than leave it, and RunInteractive with it,
// parked for as long as the orphan lives.
//
// The deadline FAILS the test, and on a miss every goroutine is dumped into
// the log so the parked frame is visible.
func TestRunInteractive_ReturnsWhileAnOrphanStillHoldsTheSlave(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	t.Cleanup(func() {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	cmd := exec.Command("sh", "-c", `(trap '' HUP; exec sleep 600) & echo $! > "$1"; printf 'child done\n'; exit 3`, "sh", pidFile)

	type result struct {
		code int
		err  error
	}
	var out syncBuffer
	done := make(chan result, 1)
	go func() {
		code, err := RunInteractive(context.Background(), cmd, nil, nil, &out, nil)
		done <- result{code, err}
	}()

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, 3, r.code, "the child's own exit is reported")
		assert.Contains(t, out.String(), "child done", "the child's output is delivered before the close")
	case <-time.After(slaveHolderReturnBound):
		var dump strings.Builder
		_ = pprof.Lookup("goroutine").WriteTo(&dump, 2)
		t.Fatalf("RunInteractive did not return within %s of its child's exit while an orphan held the slave; goroutines:\n%s", slaveHolderReturnBound, dump.String())
	}
}
