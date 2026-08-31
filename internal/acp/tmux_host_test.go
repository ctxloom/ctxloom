package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive a REAL tmux binary, not fakeTmuxRunner. That is the whole
// point of the file: every other test in this package proves the tmux ARGV
// this code builds, which cannot show whether tmux accepts it, whether the
// hosted process actually gets a terminal, or whether an exit code comes back.
// The interactive-hosting contract is exactly those three things, so a fake
// runner can only ever report that we asked correctly.
//
// Each test gets its OWN tmux socket. The shared server named by
// tmuxSocketName outlives the process that created it and is adopted by later
// runs, so a test binding to it would race every other run on this machine —
// a failure mode already measured on this project as a 30-minute hang.

// realTmux returns a runner on a socket private to this test, and registers
// its teardown. It skips (never fails) when tmux is absent: the host's
// toolchain is not what these tests are about.
func realTmux(t *testing.T) (*execTmuxRunner, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; interactive hosting is tmux-backed")
	}
	socket := "ctxloom-hosttest-" + runToken()
	r := &execTmuxRunner{socket: socket}
	t.Cleanup(func() {
		// kill-server on an already-dead server is not an error worth failing a
		// passing test over, so the result is deliberately dropped.
		_, _ = r.Run(context.Background(), "kill-server")
		// tmux does NOT unlink the socket when the server exits, so killing it
		// is only half of cleaning up. One evening of running this file left 33
		// dead socket files in the shared per-user socket directory, where they
		// are indistinguishable from live servers when triaging -- and leftover
		// tmux state has already turned a passing suite red on this project
		// once. Measured: without this line, five tests leave five sockets.
		_ = os.Remove(filepath.Join(tmuxSocketDir(), socket))
	})
	return r, socket
}

// TestHost_RunsOnARealTTY is THE test for this feature, and the one that
// separates hosting from the existing capture path.
//
// tmuxOutputWrapper (tmux_terminal.go) opens with `exec > "$1" 2>&1`, so a
// command started through terminal/create has its stdout on a FILE. An
// interactive program asking "am I on a terminal?" gets no, draws nothing, and
// cannot be driven — which is why the tmux terminal could not host the LLM UI
// even though terminal/* itself works. Hosting must give the process a real
// PTY instead.
//
// `tty` is the cheapest possible witness: it prints the terminal device name
// and exits 1 with "not a tty" when there is none. Asserting on /dev/pts/
// therefore fails against the redirect wrapper and passes only against a real
// pty — mutate host() back to redirecting stdout and this test goes red, which
// is the property that makes it worth having.
func TestHost_RunsOnARealTTY(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	h, err := l.host(context.Background(), hostSpec{Command: "sh", Args: []string{"-c", "tty"}})
	require.NoError(t, err)

	status := waitHosted(t, l, h)
	out := readHosted(t, l, h)

	assert.Contains(t, out, "/dev/pts/",
		"the hosted command must run on a real pty; got %q", out)
	assert.NotContains(t, out, "not a tty",
		"stdout was redirected away from the terminal, so this is the capture path, not hosting")
	require.NotNil(t, status)
	require.NotNil(t, status.ExitCode)
	assert.Equal(t, 0, *status.ExitCode, "`tty` exits 0 when it has a terminal")
}

// TestHost_CapturesOutputAndTrueExitCode pins the other half of the contract:
// hosting must not cost us observability. The pane is live for a human, AND
// the bytes are readable by ctxloom, AND a failing command still reports its
// own exit code.
//
// The exit code is the assertion most likely to rot into a tautology, so it is
// deliberately NOT zero: a status path that hardcodes success, loses the code,
// or reports the wrapper's own exit instead of the command's cannot produce 7.
func TestHost_CapturesOutputAndTrueExitCode(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	h, err := l.host(context.Background(), hostSpec{
		Command: "sh",
		Args:    []string{"-c", "echo HOSTED-MARKER-4b2e; exit 7"},
	})
	require.NoError(t, err)

	status := waitHosted(t, l, h)
	out := readHosted(t, l, h)

	assert.Contains(t, out, "HOSTED-MARKER-4b2e", "the pane's output must reach the capture file")
	require.NotNil(t, status)
	require.NotNil(t, status.ExitCode)
	assert.Equal(t, 7, *status.ExitCode, "the COMMAND's exit code, not the wrapper's or tmux's")
}

// TestHost_AttachTargetNamesALiveWindow: hosting exists so a human can attach
// and watch. A target that does not resolve to a real window makes the feature
// undeliverable however well the bytes are captured, and nothing else in this
// file would notice — output and exit code both come from paths that do not
// consult the window name.
func TestHost_AttachTargetNamesALiveWindow(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	// A command that stays alive, so the window is still there to be found.
	h, err := l.host(context.Background(), hostSpec{Command: "sh", Args: []string{"-c", "sleep 30"}})
	require.NoError(t, err)
	require.NotEmpty(t, h.window)

	listed, err := runner.Run(context.Background(), "list-windows", "-a", "-F", "#{session_name}:#{window_name}")
	require.NoError(t, err)
	assert.Contains(t, listed, h.window,
		"AttachTarget must name a window tmux actually has")

	l.killWindow(context.Background(), h)
}

// TestHost_EnvAndCwdReachTheProcess: hosting the LLM UI means handing it a
// working directory and environment. Both are passed to tmux as flags, and a
// flag that is built but not honoured looks identical from the outside — so
// assert the PROCESS observed them, not that the argv contained them.
func TestHost_EnvAndCwdReachTheProcess(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())
	dir := t.TempDir()

	h, err := l.host(context.Background(), hostSpec{
		Command: "sh",
		Args:    []string{"-c", "pwd; printf '%s\\n' \"$CTXLOOM_HOST_PROBE\""},
		Cwd:     dir,
		Env:     map[string]string{"CTXLOOM_HOST_PROBE": "probe-value-91c7"},
	})
	require.NoError(t, err)

	waitHosted(t, l, h)
	out := readHosted(t, l, h)

	// macOS reports /private/var for a /var temp dir, so compare the resolved
	// path rather than the string handed in.
	resolved, rerr := os.Readlink(dir)
	if rerr != nil {
		resolved = dir
	}
	assert.True(t,
		strings.Contains(out, dir) || strings.Contains(out, resolved),
		"the hosted process must start in Cwd; wanted %q in %q", dir, out)
	assert.Contains(t, out, "probe-value-91c7", "Env must reach the hosted process")
}

// waitHosted blocks until the hosted command exits, failing the test rather
// than hanging the suite if it never does.
func waitHosted(t *testing.T, l *localTerminals, h *tmuxTerminal) *api.TerminalExitStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, l.waitWindow(ctx, h))
	return readStatus(h)
}

func readHosted(t *testing.T, l *localTerminals, h *tmuxTerminal) string {
	t.Helper()
	out, err := windowOutput(h)
	require.NoError(t, err)
	return out
}

// TestHost_LaunchesARealFullScreenTUI is the end-to-end launch proof, and the
// closest thing here to the actual goal: hosting the LLM UI.
//
// top is the witness because it is not merely interactive but FULL-SCREEN — it
// refuses to start without a terminal, sizes itself to the window, and repaints
// in place. Its rendered header is therefore something the capture path could
// not produce by any accident: with stdout on a file top either dies or writes
// nothing to a pane that does not exist.
//
// The wait is a POLL rather than a sleep, and deliberately so: "has it painted
// yet" is a genuine synchronization on another process's first frame, and a
// fixed sleep would be both slower and load-sensitive. This box has run at load
// average 6 while this test passed.
func TestHost_LaunchesARealFullScreenTUI(t *testing.T) {
	if _, err := exec.LookPath("top"); err != nil {
		t.Skip("top not present; it is the full-screen witness this test needs")
	}
	runner, socket := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	h, err := l.host(context.Background(), hostSpec{Command: "top", Args: []string{"-d", "1"}})
	require.NoError(t, err)
	t.Logf("hosted; a human would attach with: tmux -L %s attach -t %s", socket, h.window)

	var pane string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		pane, err = runner.Run(context.Background(), "capture-pane", "-p", "-t", h.window)
		require.NoError(t, err)
		if strings.Contains(pane, "load average") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	assert.Contains(t, pane, "load average",
		"a full-screen TUI must actually paint into the hosted pane; got:\n%s", pane)
	assert.NotContains(t, pane, "Pane is dead",
		"top exited instead of running — it was not given a usable terminal")

	l.killWindow(context.Background(), h)
}

// tmuxSocketDir is where tmux keeps its per-user server sockets, mirroring
// tmux's own rule: $TMUX_TMPDIR when set, else /tmp/tmux-<uid>.
func tmuxSocketDir() string {
	if dir := os.Getenv("TMUX_TMPDIR"); dir != "" {
		return dir
	}
	return filepath.Join("/tmp", fmt.Sprintf("tmux-%d", os.Getuid()))
}

// TestHost_ReleaseRemovesTheCaptureFiles pins the half that host() shipped
// without: hosted terminals had a kill path but no release path, so every
// hosted terminal left its output and status files behind permanently. The
// terminal/* path has always removed both in release(); hosting simply did
// not have the equivalent.
//
// The files are a transient MAILBOX, not a log -- they exist because ctxloom
// is not the parent of the process tmux spawns and so has no fd to inherit,
// and they must outlive the pane (a dead pane's scrollback is replaced by
// tmux's "Pane is dead (status N, ...)" placeholder). Nothing reads them
// after release, so anything still on disk then is a leak.
//
// THE PRE-ASSERTIONS ARE LOAD-BEARING. "The files are gone" is trivially
// satisfied by files that were never created -- absence satisfying absence,
// which is this project's catalogued tautology. Asserting they EXIST first is
// what makes the post-condition mean something.
func TestHost_ReleaseRemovesTheCaptureFiles(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	h, err := l.host(context.Background(), hostSpec{
		Command: "sh",
		Args:    []string{"-c", "echo release-probe-8d1a"},
	})
	require.NoError(t, err)
	waitHosted(t, l, h)

	require.FileExists(t, h.outputPath, "precondition: capture file must exist before release")
	require.FileExists(t, h.statusPath, "precondition: status file must exist before release")

	l.releaseWindow(context.Background(), h)

	assert.NoFileExists(t, h.outputPath, "release must remove the capture file, as terminal/*'s release() does")
	assert.NoFileExists(t, h.statusPath, "release must remove the status file")
}

// TestHost_ReleaseIsSafeOnAnAlreadyKilledTerminal: release runs after kill in
// the ordinary teardown order, and must not report failure for work already
// done. The terminal/* path tolerates exactly this.
func TestHost_ReleaseIsSafeOnAnAlreadyKilledTerminal(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	h, err := l.host(context.Background(), hostSpec{Command: "sh", Args: []string{"-c", "sleep 30"}})
	require.NoError(t, err)
	l.killWindow(context.Background(), h)

	l.releaseWindow(context.Background(), h) // must not panic or block after a kill
	assert.NoFileExists(t, h.outputPath, "release still cleans up after a kill")
}

// TestHost_ContextCancellationReleasesTheTerminal pins the TRIGGER, not a
// caller's discipline. releaseHosted alone only fixes the leak for a caller
// that remembers to call it; the files must be reclaimed when the terminal
// stops being needed, which is when the session context ends.
//
// The trigger is context.AfterFunc, registered at host() time — an explicit
// registration against session lifetime, not a defer in whatever call frame
// happened to create the terminal. A defer would fire when THAT function
// returns, which for a backgrounded terminal is far too early, and would not
// fire at all if the session died some other way.
//
// The cleanup necessarily runs with the context ALREADY cancelled, so it must
// not use that context for its own tmux calls: execTmuxRunner builds
// exec.CommandContext, and a cancelled context kills the command before it
// runs. Mutating the WithoutCancel away makes this test fail, which is the
// point of asserting the files are gone rather than that the hook ran.
func TestHost_ContextCancellationReleasesTheTerminal(t *testing.T) {
	runner, _ := realTmux(t)
	l := newLocalTerminals(runner, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	h, err := l.host(ctx, hostSpec{Command: "sh", Args: []string{"-c", "echo trigger-probe-2e7b"}})
	require.NoError(t, err)
	waitHosted(t, l, h)

	require.FileExists(t, h.outputPath, "precondition: the file must exist before cancellation")
	require.FileExists(t, h.statusPath, "precondition: the status file must exist before cancellation")

	cancel()

	// AfterFunc runs on its own goroutine, so the EFFECT is what to wait on.
	require.Eventually(t, func() bool {
		_, oerr := os.Stat(h.outputPath)
		_, serr := os.Stat(h.statusPath)
		return os.IsNotExist(oerr) && os.IsNotExist(serr)
	}, 15*time.Second, 50*time.Millisecond,
		"ending the session must reclaim the terminal's files without anyone calling release")

	// THE WINDOW, not just the files — and this assertion is the one that
	// pins context.WithoutCancel. os.Remove ignores contexts, so the files
	// are reclaimed even by a hook that reuses the cancelled context; the
	// tmux calls are not. Reusing the cancelled context makes kill-window die
	// before it runs (execTmuxRunner builds an exec.CommandContext) and its
	// error is deliberately swallowed, so the window would leak SILENTLY with
	// every file-based assertion still green. Asserting on the files alone
	// let that mutation survive — measured, not hypothesised.
	require.Eventually(t, func() bool {
		listed, lerr := runner.Run(context.Background(), "list-windows", "-a", "-F", "#{session_name}:#{window_name}")
		return lerr != nil || !strings.Contains(listed, h.window)
	}, 15*time.Second, 50*time.Millisecond,
		"ending the session must also destroy the tmux window, not only delete its files")
}
