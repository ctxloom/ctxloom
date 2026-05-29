// Package ptyrunner provides cross-platform PTY support for running interactive commands.
package ptyrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/term"
)

// Result contains the output and exit code from running a command.
type Result struct {
	Output   string
	ExitCode int
}

// RunInteractive runs a command in interactive mode using a PTY.
// This creates a pseudo-terminal that makes the child process see a real terminal,
// enabling interactive CLI tools to work correctly even when stdin is a pipe.
func RunInteractive(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) (*Result, error) {
	// Create PTY (cross-platform: Unix PTY or Windows ConPTY)
	ptty, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create pty: %w", err)
	}
	defer func() { _ = ptty.Close() }()

	// Create command using PTY
	c := ptty.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env

	// Platform-specific command adjustments (e.g., Windows .cmd/.bat handling)
	adjustPtyCommand(c, cmd)

	// Create a done channel to signal goroutines to stop
	done := make(chan struct{})
	defer close(done)

	// Start command on PTY slave
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Handle terminal resize (platform-specific)
	stopResize := startResizeHandler(ptty)
	defer stopResize()

	// Snapshot os.Stdin once. The stdin-copy goroutine below outlives this
	// function (it parks in Read until the next input event), so it must not
	// re-read the os.Stdin global — a caller that reassigns os.Stdin afterward
	// would otherwise race the parked goroutine.
	stdin := os.Stdin

	// Set stdin to raw mode if it's a terminal
	var oldState *term.State
	stdinIsTerm := term.IsTerminal(int(stdin.Fd()))
	if stdinIsTerm {
		oldState, err = term.MakeRaw(int(stdin.Fd()))
		if err == nil {
			defer func() {
				_ = term.Restore(int(stdin.Fd()), oldState)
				// Platform-specific terminal reset (stty sane on Unix, no-op on Windows)
				if stdinIsTerm {
					resetTerminal()
				}
			}()
		}
	}

	// Copy stdin to the PTY.
	//
	// NOTE: os.Stdin.Read blocks and cannot be interrupted — we must not
	// close os.Stdin, since it is the process's shared real stdin. So when
	// the command exits and we close the PTY below, this goroutine stays
	// parked in Read until the next keystroke or EOF. On that next read it
	// either sees `done` closed or the PTY write fails (PTY now closed) and
	// returns. That is a bounded park (one goroutine until the next input
	// event), not an unbounded leak: it cannot outlive the next stdin event.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdin.Read(buf)
			if err != nil {
				return
			}
			select {
			case <-done:
				return
			default:
			}
			if n > 0 {
				// A failed write means the PTY was closed (command exited);
				// stop rather than spin so the goroutine unparks promptly.
				if _, werr := ptty.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
	}()

	// Copy PTY output to stdout in a goroutine
	var stdoutBuf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		if stdout != nil {
			_, _ = io.Copy(io.MultiWriter(os.Stdout, stdout, &stdoutBuf), ptty)
		} else {
			_, _ = io.Copy(io.MultiWriter(os.Stdout, &stdoutBuf), ptty)
		}
	}()

	// Wait for command to finish first
	err = c.Wait()

	// Close PTY to unblock the copy goroutine
	// (subprocess MCP servers may still have it open, causing io.Copy to block)
	_ = ptty.Close()

	// Wait for copy to finish
	<-copyDone

	result := &Result{
		Output:   stdoutBuf.String(),
		ExitCode: 0,
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if !isBenignPTYError(err) {
			// Anything that isn't the expected PTY-close fallout is a real
			// failure. Benign close errors are matched by sentinel, not by
			// substring of the error text.
			return nil, fmt.Errorf("command failed: %w", err)
		}
	}

	return result, nil
}

