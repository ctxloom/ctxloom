package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

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
// oneshot run forever (the same held-fd scenario ptyrunner documents for the
// interactive pty). WaitDelay makes os/exec force-close the pipes instead.
const nonInteractiveWaitDelay = 3 * time.Second

// RunLaunchSpec is the runner's process launcher — the agent.Launcher the
// runner injects into the hosted engine's Backend (agent.Hosted.Backend). It
// execs the backend's LaunchSpec: an interactive launch on a pty this runner
// owns, relayed to the runner's own terminal (the pty slave the originator
// holds the master of), a non-interactive one over pipes. Process execution
// lives here, in the runner, not in the engine.
func RunLaunchSpec(ctx context.Context, spec agent.LaunchSpec, stdin io.Reader, stdout, stderr io.Writer, resize <-chan agent.WindowSize) (int32, error) {
	cmd := exec.CommandContext(ctx, resolveBinaryPath(spec.BinaryPath), spec.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env

	if spec.Interactive {
		// The engine gets a pty of its own rather than inheriting the runner's
		// terminal, so the runner stays between the two: keystrokes pass
		// through the caller's reader, where the terminal injector types
		// coordinator mail between them, and the window sizes the caller
		// relays are applied to the engine's terminal.
		//
		// The pty merges the child's stdout and stderr into one stream, so the
		// interactive branch has a single destination: stderr is unreachable
		// here by construction and is not passed on (ptyrunner.RunInteractive
		// takes one writer). Only the non-interactive branch below can keep
		// the two apart.
		//
		// spec.StdinCleanup travels with the reader from whoever created it;
		// this layer relays it and never substitutes one, because it cannot
		// tell whether stdin is a pipe it may close or a terminal it may not.
		exitCode, err := ptyrunner.RunInteractive(ctx, cmd, stdin, spec.StdinCleanup, stdout, resize)
		if err != nil {
			return 1, fmt.Errorf("failed to run %s: %w", spec.BinaryPath, err)
		}
		return int32(exitCode), nil
	}

	// Non-interactive: stdin is the caller's reader when provided (a backend
	// delivering a large oneshot prompt pipes it here so it can't blow the argv
	// length limit), else nil so the child reads from the null device and never
	// blocks waiting for input. A finite reader hands the child EOF after the
	// prompt, so it can't hang either. Output goes ONLY to the caller's
	// writers; neither branch touches the process's own stdio.
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Held-fd hang guard: see nonInteractiveWaitDelay.
	cmd.WaitDelay = nonInteractiveWaitDelay

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Same status mapping as the interactive branch above: a child
			// killed by a signal reports 128+signum, never os/exec's raw -1,
			// which is not a valid exit status and reaches the user as a
			// truncated 255. Both launch modes must classify a killed engine
			// the same way or the exit code depends on which one ran.
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
