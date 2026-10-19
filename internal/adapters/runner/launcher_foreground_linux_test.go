//go:build linux

package runner

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// These tests put the launch in a runner process on a pty, as
// launcher_onepty_unix_test.go does, because the terminal's foreground job is
// a property of the runner's controlling terminal, which an in-process launch
// does not have. They are Linux's: it is where the runner hands that
// foreground to its engine (runOnTerminal).

// masterUint32 answers req, asked of the pty's master: the kernel answers
// questions about the slave there whoever's controlling terminal it is.
func (f *frontendPty) masterUint32(t *testing.T, req uint) int {
	t.Helper()
	up, ok := f.s.Master().(pty.UnixPty)
	require.True(t, ok, "a unix pty exposes its master")
	var v uint32
	var ioErr error
	require.NoError(t, up.Control(func(fd uintptr) { v, ioErr = unix.IoctlGetUint32(int(fd), req) }))
	require.NoError(t, ioErr)
	return int(v)
}

// foreground is the terminal's foreground process group.
func (f *frontendPty) foreground(t *testing.T) int {
	t.Helper()
	return f.masterUint32(t, unix.TIOCGPGRP)
}

// localModes is the terminal's local mode flags (c_lflag).
func (f *frontendPty) localModes(t *testing.T) uint32 {
	t.Helper()
	up, ok := f.s.Master().(pty.UnixPty)
	require.True(t, ok, "a unix pty exposes its master")
	var modes *unix.Termios
	var ioErr error
	require.NoError(t, up.Control(func(fd uintptr) { modes, ioErr = unix.IoctlGetTermios(int(fd), unix.TCGETS) }))
	require.NoError(t, ioErr)
	return modes.Lflag
}

// awaitLine blocks until the master carried a match for re and returns its
// integer submatches; the budget bounds a failure.
func (f *frontendPty) awaitLine(t *testing.T, re *regexp.Regexp) []int {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		if m := re.FindStringSubmatch(f.out.String()); m != nil {
			ints := make([]int, 0, len(m)-1)
			for _, s := range m[1:] {
				n, err := strconv.Atoi(s)
				require.NoError(t, err)
				ints = append(ints, n)
			}
			return ints
		}
		select {
		case <-f.drained:
			if re.MatchString(f.out.String()) {
				continue
			}
			t.Fatalf("the pty ended before it carried %s; it carried:\n%q", re, f.out.String())
		case <-deadline:
			t.Fatalf("the pty never carried %s; it carried:\n%q", re, f.out.String())
		case <-time.After(time.Millisecond):
		}
	}
}

// heldRunner is the line a runner helper started with holdRunnerEnv prints
// once its launch has returned.
var heldRunner = regexp.MustCompile(holdRunnerMarker + ` code=(\d+) pid=(\d+) pgrp=(\d+)`)

// enginePid is the line the scripts below print with the engine shell's pid,
// which leads the engine's process group.
var enginePid = regexp.MustCompile(`ENGINE pid=(\d+)\r\n`)

// A ^C typed while the engine's line discipline is in effect — before a TUI
// engine has made its input raw, or an engine that never does — interrupts
// the engine and nothing else. The engine is its terminal's foreground job, so
// the kernel aims the SIGINT at the engine's group, not at the runner's: the
// runner survives it and reports the engine's own exit. The runner helper
// leaves SIGINT at its default, so a ^C that reached it would end it with 130.
func TestRunLaunchSpec_ATypedInterruptReachesTheEngineAndNotTheRunner(t *testing.T) {
	const script = `trap 'echo ENGINE-GOT-INT; kill $! 2>/dev/null; exit 7' INT
sleep 30 &
echo READY
wait $!`
	f := startRunnerOnAPty(t, script)
	require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
	f.waitFor(t, "READY")
	_, err := f.s.Master().Write([]byte{0x03})
	require.NoError(t, err)
	f.waitFor(t, "ENGINE-GOT-INT")
	assert.Equal(t, 7, f.exit(t), "the runner survives the interrupt and exits with the engine's status")
}

// The engine is its terminal's foreground job for its own lifetime and no
// longer: while it runs, the terminal's foreground process group is the
// engine's; once it has exited the runner has taken the terminal back — its
// own group is the foreground again — and put back the raw input the terminal
// carries bytes in.
func TestRunLaunchSpec_TheEngineIsTheForegroundJobUntilItExits(t *testing.T) {
	const script = `echo "ENGINE pid=$$"
IFS= read -r line
exit 3`
	f := startRunnerOnAPty(t, script, holdRunnerEnv+"=1")
	require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
	engine := f.awaitLine(t, enginePid)[0]
	assert.Equal(t, engine, f.foreground(t), "while the engine runs, its group is the terminal's foreground job")

	_, err := f.s.Master().Write([]byte("done\n"))
	require.NoError(t, err)
	held := f.awaitLine(t, heldRunner)
	code, runner, runnerGroup := held[0], held[1], held[2]
	assert.Equal(t, 3, code)
	assert.NotEqual(t, engine, runnerGroup, "the engine ran in a group of its own")
	assert.Equal(t, runnerGroup, f.foreground(t), "once the engine has exited, the runner's group is the terminal's foreground job again")
	assert.Zero(t, f.localModes(t)&(unix.ICANON|unix.ECHO|unix.ISIG), "the runner's raw input is back")

	require.NoError(t, syscall.Kill(runner, syscall.SIGUSR1))
	assert.Equal(t, 3, f.exit(t))
}

// A start that fails once the engine's process exists — here its binary is
// nothing the kernel can execute — has already handed the terminal to the
// group that process led, which takes the terminal before it execs. The
// runner takes its terminal back all the same, puts its raw input back, and
// reports the failure.
func TestRunLaunchSpec_AFailedStartLeavesTheRunnerHoldingItsTerminal(t *testing.T) {
	notAProgram := filepath.Join(t.TempDir(), "not-a-program")
	require.NoError(t, os.WriteFile(notAProgram, []byte("not a program\n"), 0o755))
	f := startRunnerOnAPty(t, "", holdRunnerEnv+"=1", launchBinaryEnv+"="+notAProgram)
	require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
	held := f.awaitLine(t, heldRunner)
	code, runner, runnerGroup := held[0], held[1], held[2]
	assert.Equal(t, 97, code, "the launch reports that its engine never started")
	assert.Contains(t, f.out.String(), "exec format error", "the engine's process existed and failed to exec")
	assert.Equal(t, runnerGroup, f.foreground(t), "the runner's group is the terminal's foreground job again")
	assert.Zero(t, f.localModes(t)&(unix.ICANON|unix.ECHO|unix.ISIG), "the runner's raw input is back")

	require.NoError(t, syscall.Kill(runner, syscall.SIGUSR1))
	assert.Equal(t, 97, f.exit(t))
}

// The foreground job is made of the terminal the launch is handed, whichever
// descriptor carries it: here the runner's own stdin is the null device, and
// the engine is still the foreground job of the terminal it runs on.
func TestRunLaunchSpec_TheForegroundIsTheTerminalTheLaunchIsHanded(t *testing.T) {
	const script = `echo "ENGINE pid=$$"
IFS= read -r line
exit 6`
	f := startRunnerOnAPty(t, script, terminalOffStdinEnv+"=1")
	require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
	engine := f.awaitLine(t, enginePid)[0]
	assert.Equal(t, engine, f.foreground(t), "the engine's group is the foreground job of the terminal it was handed")
	_, err := f.s.Master().Write([]byte("done\n"))
	require.NoError(t, err)
	assert.Equal(t, 6, f.exit(t))
}

// A suspended engine is continued, and its whole group with it. The runner
// has no job control to offer — no prompt to return to, no `fg` — so an
// engine stopped by a ^Z typed in its line discipline, or by suspending its
// own group as claude does on ^Z (kill(0, SIGTSTP)), would hold the terminal
// stopped with nothing to wake it. The kernel discarded those stops while the
// engine led a session of its own on a pty of its own; as the terminal's
// foreground job it would stay stopped, so the runner continues it. In the
// second case a child the engine waits on suspends the group, so continuing
// the engine alone would leave it waiting on a child that never runs again.
func TestRunLaunchSpec_ASuspendedEngineIsContinued(t *testing.T) {
	for _, tc := range []struct {
		name      string
		script    string
		suspend   []byte
		continued []string
	}{
		{
			name: "a ^Z typed in its line discipline",
			script: `trap 'echo ENGINE-CONTINUED; kill $! 2>/dev/null; exit 4' CONT
sleep 30 &
echo READY
wait $!`,
			suspend:   []byte{0x1a},
			continued: []string{"ENGINE-CONTINUED"},
		},
		{
			name: "suspending its own group",
			script: `trap 'echo ENGINE-CONTINUED' CONT
echo READY
sh -c 'kill -TSTP 0; echo CHILD-CONTINUED'
exit 4`,
			continued: []string{"CHILD-CONTINUED", "ENGINE-CONTINUED"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startRunnerOnAPty(t, tc.script)
			require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
			f.waitFor(t, "READY")
			if tc.suspend != nil {
				_, err := f.s.Master().Write(tc.suspend)
				require.NoError(t, err)
			}
			for _, want := range tc.continued {
				f.waitFor(t, want)
			}
			assert.Equal(t, 4, f.exit(t), "the continued engine runs on to its own exit")
		})
	}
}

// What the engine leaves in its group when it exits — a tool or server it
// started there — is hung up then, as the kernel hangs up a session's
// foreground job when the session's leader exits. The runner's own exit
// would not reach it: by then the runner has taken the terminal back, and the
// kernel hangs up only the foreground job. The runner is held past its launch
// here, so only the hangup at the engine's exit can reach the holder.
func TestRunLaunchSpec_WhatTheEngineLeftInItsGroupIsHungUpAtItsExit(t *testing.T) {
	const script = `sh -c 'trap "echo HOLDER-HUNG-UP; exit 0" HUP; echo HOLDER-UP; sleep 30 & wait' &
echo "ENGINE pid=$$"
IFS= read -r line
exit 0`
	f := startRunnerOnAPty(t, script, holdRunnerEnv+"=1")
	require.NoError(t, f.s.Resize(24, 80), "the frontend's first size")
	engine := f.awaitLine(t, enginePid)[0]
	t.Cleanup(func() { _ = syscall.Kill(-engine, syscall.SIGKILL) })
	f.waitFor(t, "HOLDER-UP")

	_, err := f.s.Master().Write([]byte("\n"))
	require.NoError(t, err)
	f.waitFor(t, "HOLDER-HUNG-UP")
	held := f.awaitLine(t, heldRunner)
	require.NoError(t, syscall.Kill(held[1], syscall.SIGUSR1))
	assert.Equal(t, 0, f.exit(t))
}
