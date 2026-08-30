package backends

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// RunLaunchSpec is ctxloom's process launcher — the pty-backed agent.Launcher
// injected into every local-CLI backend at registry construction. It execs the
// agent's LaunchSpec, allocating a pty for interactive sessions so the child CLI
// sees a terminal even when stdin is a pipe (go-plugin gRPC). Process execution
// lives here in the runtime, not in the engine-agnostic substrate.
func RunLaunchSpec(ctx context.Context, spec agent.LaunchSpec, stdin io.Reader, stdout, stderr io.Writer, resize <-chan agent.WindowSize) (int32, error) {
	cmd := exec.CommandContext(ctx, resolveBinaryPath(spec.BinaryPath), spec.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env

	if spec.Interactive {
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
	// prompt, so it can't hang either. Output goes ONLY to the caller's writers
	// (the gRPC stream) — this code runs inside the go-plugin subprocess, whose
	// own stdout is discarded by the host and whose stderr is re-surfaced through
	// the plugin logger, so teeing to os.Stdout/os.Stderr was dead weight that
	// could double-print child stderr under -v. The interactive branch likewise
	// never touches the process's own stdio (the frontend renders).
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
