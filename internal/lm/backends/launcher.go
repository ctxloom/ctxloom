package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/ctxloom/ctxloom/internal/ptyrunner"
	"github.com/ctxloom/shared/agent"
)

// RunLaunchSpec is ctxloom's process launcher — the pty-backed agent.Launcher
// injected into every local-CLI backend at registry construction. It execs the
// agent's LaunchSpec, allocating a pty for interactive sessions so the child CLI
// sees a terminal even when stdin is a pipe (go-plugin gRPC). Process execution
// lives here in the runtime, not in the engine-agnostic substrate.
func RunLaunchSpec(ctx context.Context, spec agent.LaunchSpec, stdin io.Reader, stdout, stderr io.Writer, resize <-chan agent.WindowSize) (int32, error) {
	cmd := exec.CommandContext(ctx, spec.BinaryPath, spec.Args...)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env

	if spec.Interactive {
		result, err := ptyrunner.RunInteractive(ctx, cmd, stdin, stdout, stderr, resize)
		if err != nil {
			return 1, fmt.Errorf("failed to run %s: %w", spec.BinaryPath, err)
		}
		return int32(result.ExitCode), nil
	}

	// Non-interactive: no stdin (don't wait for input); tee child output to the
	// process's own stdout/stderr and the caller's writers.
	cmd.Stdin = nil
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(os.Stdout, stdout)
	} else {
		cmd.Stdout = os.Stdout
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	} else {
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return int32(exitErr.ExitCode()), nil
		}
		return 1, fmt.Errorf("failed to run %s: %w", spec.BinaryPath, err)
	}
	return 0, nil
}
