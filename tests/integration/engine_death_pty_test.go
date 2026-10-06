//go:build integration && linux

package integration

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport/procalive"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// engineDeathExitBound is how long the runner and `ctxloom run` may each take
// to finish once the engine is gone: the runner's own pty drain, its report
// and teardown, then the run's drain, outcome read and coordinator teardown.
// Every one of those is bounded in seconds; the defect this guards held both
// processes for as long as nobody intervened.
const engineDeathExitBound = 20 * time.Second

// engineDeathDumpWait bounds the wait for a SIGQUIT goroutine dump to reach
// the pty capture before the next process is dumped.
const engineDeathDumpWait = 5 * time.Second

// fakeLongLivedClaudeBody is a fake `claude` that announces its own pid (so
// the test kills exactly the engine, not a process it found by shape), then
// reflects typed lines until it is killed. %s is claude's version floor; the
// second verb is whatever the engine starts before it announces itself.
const fakeLongLivedClaudeBody = `#!/bin/sh
case "$1" in --version) echo "%s (Claude Code)"; exit 0;; esac
%s
echo "FAKE-ENGINE-PID:$$"
while IFS= read -r line; do echo "FAKE-ENGINE-GOT:$line"; done
`

// grandchildHoldingTheTerminal is what a crashed claude leaves behind: a
// process it started (a stdio MCP server, a shell tool) that inherited the
// engine's terminal and outlives it. It ignores SIGHUP, as such a process may:
// the kernel hangs up the dead session leader's terminal, which would
// otherwise end it and hide the case. It announces its pid so the test can
// end it once the assertions are made.
const grandchildHoldingTheTerminal = `(trap '' HUP; exec sleep 600) &
echo "FAKE-GRANDCHILD-PID:$!"`

var (
	enginePIDLine     = regexp.MustCompile(`FAKE-ENGINE-PID:(\d+)`)
	grandchildPIDLine = regexp.MustCompile(`FAKE-GRANDCHILD-PID:(\d+)`)
)

// announcedPID waits for the pid a fake engine announced on the pty.
func announcedPID(t *testing.T, sess *testenv.PTYSession, line *regexp.Regexp) int {
	t.Helper()
	var pid int
	out, announced := sess.AwaitOutput(t, func(out string) bool {
		m := line.FindStringSubmatch(out)
		if m == nil {
			return false
		}
		pid, _ = strconv.Atoi(m[1])
		return true
	})
	require.True(t, announced, "no %s announced; captured: %q", line, out)
	return pid
}

// TestRunPTY_EngineKilledMidSessionEndsTheRunnerAndTheRun is magnetic-usage's
// settling test. The engine child of a live interactive session is killed
// mid-session, and BOTH layers above it must finish on their own: the runner
// (`ctxloom runner claude-code`, the engine's parent) must exit, and
// `ctxloom run` must exit with the engine's status and release its root's
// owner lock. The deadline FAILS the test; on a miss each surviving process
// is SIGQUIT'd so its goroutine dump lands in the log, which is how the
// blocker is named.
func TestRunPTY_EngineKilledMidSessionEndsTheRunnerAndTheRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string
	}{
		{name: "engine alone"},
		{name: "engine leaves a grandchild holding its terminal", before: grandchildHoldingTheTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, sess := startInteractiveFakeClaude(t, fmt.Sprintf(fakeLongLivedClaudeBody, "%s", tc.before))
			if tc.before != "" {
				grandchild := announcedPID(t, sess, grandchildPIDLine)
				t.Cleanup(func() { testenv.KillPids([]int{grandchild}) })
			}
			engine := announcedPID(t, sess, enginePIDLine)

			// Mid-session: a typed line round-trips through the run, the
			// runner and the engine before anything is killed.
			_, err := sess.Write([]byte("before-the-crash\r"))
			require.NoError(t, err)
			out, carried := sess.AwaitOutput(t, func(out string) bool {
				return strings.Contains(out, "FAKE-ENGINE-GOT:before-the-crash")
			})
			require.True(t, carried, "the session never carried a line to the engine; captured: %q", out)

			runners := testenv.RunnerChildrenOf(sess.PID())
			require.Len(t, runners, 1, "exactly one runner serves the session")
			runner := runners[0]
			locks := ownerLocks(t, env.HomeDir)
			require.Len(t, locks, 1, "the session owns exactly one root")
			owner, err := coord.ProbeOwner(filepath.Dir(locks[0]))
			require.NoError(t, err)
			require.True(t, owner.Held, "the live session holds its root")
			require.Equal(t, sess.PID(), owner.PID, "the root is stamped with the run's pid")

			require.NoError(t, syscall.Kill(engine, syscall.SIGKILL), "kill the engine")

			runnerGone := waitGone(runner, engineDeathExitBound)
			exited, _ := sess.Wait(engineDeathExitBound)
			if !runnerGone || !exited {
				dumpGoroutines(t, sess, runner, runnerGone, exited)
			}
			require.True(t, runnerGone, "the runner (pid %d) outlived its engine by %s", runner, engineDeathExitBound)
			require.True(t, exited, "ctxloom run (pid %d) outlived its engine by %s", sess.PID(), engineDeathExitBound)
			assert.Equal(t, 128+int(syscall.SIGKILL), sess.ExitCode(),
				"the run exits with the killed engine's status; captured: %q", sess.Output())
			released, err := coord.ProbeOwner(filepath.Dir(locks[0]))
			require.NoError(t, err)
			assert.False(t, released.Held, "the run released its root's owner lock")
		})
	}
}

// waitGone waits up to bound for pid to leave the process table (a zombie
// counts as gone: its parent's reap is the parent's business).
func waitGone(pid int, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for procalive.Alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollTick)
	}
	return true
}

// pollTick paces waitGone's process-table poll.
const pollTick = 10 * time.Millisecond

// dumpGoroutines SIGQUITs whichever of the runner and the run is still alive,
// the runner first: while the run still drives the session its pty relay
// carries the runner's dump to the capture, and a run already past its drive
// writes its own dump straight to the capture. Each dump is waited for up to
// engineDeathDumpWait, then the whole capture is logged.
func dumpGoroutines(t *testing.T, sess *testenv.PTYSession, runner int, runnerGone, runExited bool) {
	t.Helper()
	if !runnerGone {
		_ = syscall.Kill(runner, syscall.SIGQUIT)
		_ = waitGone(runner, engineDeathDumpWait)
	}
	if !runExited {
		_ = syscall.Kill(sess.PID(), syscall.SIGQUIT)
		_, _ = sess.Wait(engineDeathDumpWait)
	}
	t.Logf("runner gone: %v, run exited: %v; pty capture with goroutine dumps:\n%s", runnerGone, runExited, sess.Output())
}

// TestRunPTY_RunnerDeadWithoutAnOutcomeEndsTheRun is the incident's second
// half: the runner dies without ever reporting the run's outcome — SIGQUIT,
// as the operator sent it, which ends a Go process with no teardown at all.
// `ctxloom run` must still finish: exit non-zero and release its root's owner
// lock. The second case is the incident's whole state at that moment: the
// engine already dead, a process it left holding its terminal still alive,
// and the runner ended inside its drain, before it could report.
func TestRunPTY_RunnerDeadWithoutAnOutcomeEndsTheRun(t *testing.T) {
	for _, tc := range []struct {
		name        string
		before      string
		killEngine  bool
		saysOutcome bool
	}{
		{name: "engine alive", saysOutcome: true},
		{name: "engine dead and its terminal still held", before: grandchildHoldingTheTerminal, killEngine: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, sess := startInteractiveFakeClaude(t, fmt.Sprintf(fakeLongLivedClaudeBody, "%s", tc.before))
			if tc.before != "" {
				grandchild := announcedPID(t, sess, grandchildPIDLine)
				t.Cleanup(func() { testenv.KillPids([]int{grandchild}) })
			}
			engine := announcedPID(t, sess, enginePIDLine)
			runners := testenv.RunnerChildrenOf(sess.PID())
			require.Len(t, runners, 1, "exactly one runner serves the session")
			locks := ownerLocks(t, env.HomeDir)
			require.Len(t, locks, 1, "the session owns exactly one root")

			if tc.killEngine {
				require.NoError(t, syscall.Kill(engine, syscall.SIGKILL), "kill the engine")
			}
			require.NoError(t, syscall.Kill(runners[0], syscall.SIGQUIT), "end the runner with no teardown")

			exited, _ := sess.Wait(engineDeathExitBound)
			if !exited {
				dumpGoroutines(t, sess, runners[0], true, exited)
			}
			require.True(t, exited, "ctxloom run (pid %d) outlived its runner by %s", sess.PID(), engineDeathExitBound)
			assert.NotEqual(t, 0, sess.ExitCode(), "a run whose runner died unreported is not a success; captured: %q", sess.Output())
			if tc.saysOutcome {
				assert.Contains(t, sess.Output(), "never reported its outcome", "the run says why it ended")
			}
			released, err := coord.ProbeOwner(filepath.Dir(locks[0]))
			require.NoError(t, err)
			assert.False(t, released.Held, "the run released its root's owner lock")
		})
	}
}
