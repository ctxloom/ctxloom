package procsig

import (
	"bufio"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helperEnv arms TestHelperSignalReporter; nothing else sets it.
const helperEnv = "CTXLOOM_PROCSIG_HELPER"

// TestHelperSignalReporter is not a real test — it is the re-exec target the
// tests below spawn via os.Args[0] (the standard library's TestHelperProcess
// idiom). It says "ready" once its handler is installed, then names the FIRST
// signal it receives and exits 0. Go maps CTRL_BREAK_EVENT to os.Interrupt on
// Windows, so the same reporter proves both twins.
func TestHelperSignalReporter(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("re-exec target only")
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	_, _ = os.Stdout.WriteString("ready\n")
	select {
	case sig := <-ch:
		_, _ = os.Stdout.WriteString("got " + sig.String() + "\n")
		os.Exit(0)
	case <-time.After(10 * time.Second):
		os.Exit(3)
	}
}

// spawnReporter starts the reporter under SpawnAttr and waits for it to be
// listening — the signal is only sent once the handler is provably armed.
func spawnReporter(t *testing.T) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSignalReporter$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	cmd.SysProcAttr = SpawnAttr()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready", strings.TrimSpace(line))
	return cmd, r
}

// TestInterrupt_DeliversTheInterruptSignal: Interrupt is the polite ask — the
// child sees an INTERRUPT (SIGINT on unix, CTRL_BREAK → os.Interrupt on
// Windows), not a termination, and gets to unwind.
func TestInterrupt_DeliversTheInterruptSignal(t *testing.T) {
	cmd, r := spawnReporter(t)
	require.NoError(t, Interrupt(cmd.Process))
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "got "+os.Interrupt.String(), strings.TrimSpace(line))
	require.NoError(t, cmd.Wait(), "the child unwound on its own")
}
