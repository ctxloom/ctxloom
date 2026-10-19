package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	xterm "golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
	"github.com/ctxloom/ctxloom/internal/shared/ptyrunner"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// nonInteractiveWaitDelay bounds how long a non-interactive run waits for its
// stdout/stderr pipes to drain after the main process exits. The writers are
// stream writers (not *os.File), so os/exec creates pipes — and Wait blocks
// until EOF, i.e. until every descendant that inherited the fd exits. A
// backend CLI that spawns a stdio MCP server which outlives it would hang the
// oneshot run forever. WaitDelay makes os/exec force-close the pipes instead.
// An interactive run has no pipes to wait on: its engine inherits the
// terminal's own descriptors.
const nonInteractiveWaitDelay = 3 * time.Second

// terminalSizeWait bounds how long an interactive launch holds its engine for
// the terminal's first window size. The pty `ctxloom run` starts the runner on
// is created unsized (hostpty), and the frontend sizes it only once it has
// taken the human's terminal over, which can land after the runner reaches
// the launch; an engine started before that reads 0x0 for its first paint,
// and SIGWINCH fires only on a change, so a first paint at a wrong size that
// happens to match the real one never heals. The bound exists for a frontend
// with no terminal of its own (its stdin not a tty), which never sizes the
// pty: the engine then starts on it as it is. A var so a test can shorten it.
var terminalSizeWait = 2 * time.Second

// terminalSizePoll paces waitForTerminalSize's reads of the window size.
const terminalSizePoll = 5 * time.Millisecond

// awaitTerminalSize is the first-paint wait (waitForTerminalSize). It is a
// package variable solely so a test can see the launch parked on it before it
// sizes the terminal — the only way to force the order the wait exists for;
// production never reassigns it.
var awaitTerminalSize = waitForTerminalSize

// errNoTerminal refuses an interactive launch that names no terminal.
var errNoTerminal = errors.New("an interactive launch runs its engine on a terminal, and none was given")

// RunLaunchSpec is the runner's process launcher — the agent.Launcher the
// runner injects into the hosted engine's Backend (agent.Hosted.Backend). It
// execs the backend's LaunchSpec: an interactive launch with the spec's
// terminal as the engine's stdio — the runner's own, the pty slave the
// originator holds the master of or a container's -it tty — a
// non-interactive one over pipes. Process execution lives here, in the
// runner, not in the engine.
func RunLaunchSpec(ctx context.Context, spec agent.LaunchSpec, stdin io.Reader, stdout, stderr io.Writer) (int32, error) {
	cmd := exec.CommandContext(ctx, resolveBinaryPath(spec.BinaryPath), spec.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env
	if err := startBatchViaCmdExe(cmd); err != nil {
		return 1, fmt.Errorf("failed to run %s: %w", spec.BinaryPath, err)
	}

	run := cmd.Run
	if spec.Interactive {
		release, err := onTerminal(ctx, cmd, spec.Terminal)
		if err != nil {
			return 1, fmt.Errorf("failed to run %s: %w", spec.BinaryPath, err)
		}
		defer release()
		run = func() error { return runOnTerminal(cmd, spec.Terminal.Stdin) }
	} else {
		// Non-interactive: stdin is the caller's reader when provided (a
		// backend delivering a large oneshot prompt pipes it here so it can't
		// blow the argv length limit), else nil so the child reads from the
		// null device and never blocks waiting for input. A finite reader
		// hands the child EOF after the prompt, so it can't hang either.
		// Output goes ONLY to the caller's writers.
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		// Held-fd hang guard: see nonInteractiveWaitDelay.
		cmd.WaitDelay = nonInteractiveWaitDelay
	}

	if err := run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// A child killed by a signal reports 128+signum, never os/exec's
			// raw -1, which is not a valid exit status and reaches the user as
			// a truncated 255 — in both launch modes, so the exit code does not
			// depend on which one ran.
			return int32(exitstatus.Of(exitErr)), nil
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			// The process itself succeeded; only its output pipes were still
			// held open (by a surviving descendant) when WaitDelay expired and
			// force-closed them. Not a failure.
			return 0, nil
		}
		return 1, fmt.Errorf("failed to run %s: %w", spec.BinaryPath, err)
	}
	return 0, nil
}

// onTerminal readies cmd to run on term: the engine inherits the terminal's
// three streams as they are — no pty and no relay of the runner's own, so a
// keystroke, a resize and the engine's every byte cross the one pty the
// frontend holds, and nothing in the runner reads the terminal — once the
// terminal has its first size (terminalSizeWait). The engine stays in the
// runner's session on the runner's controlling terminal, so the kernel
// delivers each resize to it as a SIGWINCH and hangs it up with the runner;
// on Linux it runs as that terminal's foreground job (runOnTerminal), so the
// signals a typed key raises reach it and not the runner.
//
// The engine finds the line discipline a new terminal has: the runner's was
// made raw for carrying bytes (hostpty), and an engine that reads its
// terminal as it finds it — a line at a time, with Enter arriving as CR —
// would wait on that forever. The returned release puts the runner's mode
// back once the engine has exited. Its failure is not reported: the runner
// ends with its engine, and a mode that stays costs only the echo of what is
// typed in the moments before the frontend drops the pty.
func onTerminal(ctx context.Context, cmd *exec.Cmd, term *agent.Terminal) (release func(), err error) {
	if term == nil || term.Stdin == nil || term.Stdout == nil || term.Stderr == nil {
		return nil, errNoTerminal
	}
	for _, f := range []*os.File{term.Stdin, term.Stdout} {
		if !xterm.IsTerminal(int(f.Fd())) {
			return nil, fmt.Errorf("an interactive launch runs its engine on a terminal, and %s is not one", f.Name())
		}
	}
	if err := awaitTerminalSize(ctx, term.Stdout); err != nil {
		return nil, fmt.Errorf("the run ended before the terminal was sized: %w", err)
	}
	restore, err := ptyrunner.FreshLineDiscipline(term.Stdin)
	if err != nil {
		return nil, fmt.Errorf("give the engine a new terminal's line discipline: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = term.Stdin, term.Stdout, term.Stderr
	return func() { _ = restore() }, nil
}

// waitForTerminalSize returns once f's terminal reports a non-zero window
// size, terminalSizeWait has passed, or ctx is done (its error). It reads the
// size rather than waiting for SIGWINCH: the read is the terminal's state
// whoever's controlling terminal it is, and a signal is delivered only to the
// terminal's foreground process group. A size that cannot be read at all is
// nothing to wait for.
func waitForTerminalSize(ctx context.Context, f *os.File) error {
	deadline := time.NewTimer(terminalSizeWait)
	defer deadline.Stop()
	tick := time.NewTicker(terminalSizePoll)
	defer tick.Stop()
	for {
		w, h, err := xterm.GetSize(int(f.Fd()))
		if err != nil || (w > 0 && h > 0) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-tick.C:
		}
	}
}

// resolveBinaryPath resolves an engine's configured binary name to a path
// exec can spawn, falling back to the user's login-shell PATH (shellenv)
// when the process's own inherited PATH doesn't have it — the GUI-launch
// class of bug (a minimal or even empty $SHELL/$PATH) ctxloom-vscode already
// fixed for its OWN companion spawns; this closes the same gap one process
// down, for the engine binary ctxloom itself launches under `ctxloom run`.
// Never fails outright: an unresolvable name is passed through UNCHANGED so
// os/exec's own ENOENT surfaces exactly as it would have before — this only
// ever WIDENS what resolves, never narrows or hides a genuine "not
// installed" failure.
func resolveBinaryPath(name string) string {
	if resolved, err := shellenv.Resolve(name); err == nil {
		return resolved
	}
	return name
}
