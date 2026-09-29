//go:build integration && linux

package integration

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// ownerDeathExitBound is how long a session whose terminal or runner is gone
// may take to exit: the drive's own bounds (the drain grace, End then Kill)
// plus the coordinator's teardown.
const ownerDeathExitBound = 20 * time.Second

const ownerDeathSentinel = "owner-death-sentinel"

// startParkedSession starts an interactive run whose mock engine parks in its
// echo loop, and waits until it is up.
func startParkedSession(t *testing.T) *testenv.PTYSession {
	t.Helper()
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)
	writeFragment(t, env, "owner-death-fragment", nil, "owner death test content")

	bin := env.AppBinary
	if b := os.Getenv("CTXLOOM_OWNER_DEATH_BIN"); b != "" {
		bin = b
	}
	s, err := env.RunPTYFrom(bin, 80, 24, []string{"CTXLOOM_MOCK_ECHO_STDIN=1"}, "run", "-f", "owner-death-fragment")
	require.NoError(t, err)
	t.Cleanup(s.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("session pty output: %s", s.Output())
		}
	})
	_, err = s.Write([]byte(ownerDeathSentinel + "\n"))
	require.NoError(t, err)
	require.True(t, s.WaitForOutput(ownerRefusalPollTimeout, func(out string) bool {
		return strings.Contains(out, "mock echo: "+ownerDeathSentinel)
	}), "the session never came up; output:\n%s", s.Output())
	return s
}

// A terminal that dies under an interactive run must end the run: SIGHUP is
// one of the shutdown signals the run absorbs, and a run that absorbs it
// without ending keeps owning its project with nothing attached to it.
func TestInteractiveRun_ExitsWhenItsTerminalHangsUp(t *testing.T) {
	s := startParkedSession(t)
	require.NoError(t, s.Hangup())
	exited, _ := s.Wait(ownerDeathExitBound)
	require.True(t, exited, "the run outlived its terminal by %s", ownerDeathExitBound)
}

// A run whose terminal died and whose runner then exits must exit too — the
// incident's second half, where ending the engine and its runner still left
// the orphaned run alive.
func TestInteractiveRun_ExitsWhenItsRunnerDiesAfterHangup(t *testing.T) {
	s := startParkedSession(t)
	runners := testenv.PluginChildrenOf(s.PID())
	require.NotEmpty(t, runners, "the run's runner child was not found")
	require.NoError(t, s.Hangup())
	time.Sleep(2 * time.Second) // let the hangup land before the runner dies
	for _, pid := range runners {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	exited, _ := s.Wait(ownerDeathExitBound)
	require.True(t, exited, "the run outlived its runner by %s", ownerDeathExitBound)
}

// Control: a runner that dies under a run whose terminal is intact ends the
// run through the drive's ordinary exit path.
func TestInteractiveRun_ExitsWhenItsRunnerDies(t *testing.T) {
	s := startParkedSession(t)
	runners := testenv.PluginChildrenOf(s.PID())
	require.NotEmpty(t, runners, "the run's runner child was not found")
	for _, pid := range runners {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	exited, _ := s.Wait(ownerDeathExitBound)
	require.True(t, exited, "the run outlived its runner by %s", ownerDeathExitBound)
}
