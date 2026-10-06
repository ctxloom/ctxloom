//go:build integration && linux

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

const ownerDeathSentinel = "owner-death-sentinel"

// startParkedSession starts an interactive run whose mock engine parks in its
// echo loop, and waits until it is up.
func startParkedSession(t *testing.T) (*testenv.PTYSession, *testenv.TestEnvironment) {
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
	out, up := s.AwaitOutput(t, func(out string) bool {
		return strings.Contains(out, "mock echo: "+ownerDeathSentinel)
	})
	require.True(t, up, "the session never came up; output:\n%s", out)
	return s, env
}

// A terminal that dies under an interactive run must end the run: SIGHUP is
// one of the shutdown signals the run absorbs, and a run that absorbs it
// without ending keeps owning its project with nothing attached to it.
func TestInteractiveRun_ExitsWhenItsTerminalHangsUp(t *testing.T) {
	s, _ := startParkedSession(t)
	require.NoError(t, s.Hangup())
	exited, _ := s.AwaitExit(t)
	require.True(t, exited, "the run outlived its terminal; output:\n%s", s.Output())
}

// A run whose terminal died and whose runner then exits must exit too — the
// incident's second half, where ending the engine and its runner still left
// the orphaned run alive.
func TestInteractiveRun_ExitsWhenItsRunnerDiesAfterHangup(t *testing.T) {
	s, env := startParkedSession(t)
	runners := testenv.RunnerChildrenOf(s.PID())
	require.NotEmpty(t, runners, "the run's runner child was not found")
	require.NoError(t, s.Hangup())
	// The runner must die AFTER the run has absorbed the hangup, or this is
	// the runner-dies case below. The terminal is gone, so the run's notice is
	// read from the session's diagnostics log, where the terminal UI diverts it.
	sessions := filepath.Join(env.HomeDir, paths.AppDirName, paths.SessionsDir)
	notice := fmt.Sprintf(cli.ShutdownSignalNotice, syscall.SIGHUP)
	_, received := testenv.AwaitFileContaining(t, sessions, paths.DiagnosticsLogFileName, notice)
	require.True(t, received, "the run never recorded %q in a session diagnostics log", notice)
	for _, pid := range runners {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	exited, _ := s.AwaitExit(t)
	require.True(t, exited, "the run outlived its runner; output:\n%s", s.Output())
}

// Control: a runner that dies under a run whose terminal is intact ends the
// run through the drive's ordinary exit path.
func TestInteractiveRun_ExitsWhenItsRunnerDies(t *testing.T) {
	s, _ := startParkedSession(t)
	runners := testenv.RunnerChildrenOf(s.PID())
	require.NotEmpty(t, runners, "the run's runner child was not found")
	for _, pid := range runners {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	exited, _ := s.AwaitExit(t)
	require.True(t, exited, "the run outlived its runner; output:\n%s", s.Output())
}
