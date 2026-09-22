package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
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
// execs the backend's LaunchSpec: an interactive launch in a tmux pane on
// the runner's own terminal (the pty slave the originator holds the master
// of), a non-interactive one over pipes. Process execution lives here, in
// the runner, not in the engine.
func RunLaunchSpec(ctx context.Context, spec agent.LaunchSpec, stdin io.Reader, stdout, stderr io.Writer, resize <-chan agent.WindowSize) (int32, error) {
	cmd := exec.CommandContext(ctx, resolveBinaryPath(spec.BinaryPath), spec.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env

	if spec.Interactive {
		// ONE interactive path: the engine is hosted in a tmux pane, so a
		// human can attach to a run already in progress. There is no pty
		// fallback when tmux is missing -- see panelaunch.go's doc for why a
		// fallback is what would make the dependency untrue.
		//
		// The pane merges the child's stdout and stderr onto one real pty, so
		// this branch has a single destination: stderr is unreachable here by
		// construction and is not passed on. Only the non-interactive branch
		// below can keep the two apart.
		//
		// spec.StdinCleanup travels with the reader from whoever created it;
		// this layer relays it and never substitutes one, because it cannot
		// tell whether stdin is a pipe it may close or a terminal it may not.
		return runInteractiveInPane(ctx, spec, stdin, stdout, resize)
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
			return int32(ptyrunner.ExitStatusFor(exitErr)), nil
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
