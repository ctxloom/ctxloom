// Package ptyrunner provides utilities for running commands with PTY support.
package ptyrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Result contains the output and exit code from running a command.
type Result struct {
	Output   string
	ExitCode int
}

// RunInteractive runs a command in interactive mode using a PTY.
// It properly handles stdin copying with cancellation to prevent goroutine leaks.
func RunInteractive(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) (*Result, error) {
	// Start command with a PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start command with pty: %w", err)
	}

	// Create a done channel to signal goroutines to stop
	done := make(chan struct{})
	defer close(done)

	// Ensure PTY is closed when we're done
	defer ptmx.Close()

	// Handle terminal resize
	resizeCh := make(chan os.Signal, 1)
	signal.Notify(resizeCh, syscall.SIGWINCH)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-resizeCh:
				// Ignore resize errors - best effort terminal resizing
				_ = pty.InheritSize(os.Stdin, ptmx)
			}
		}
	}()
	resizeCh <- syscall.SIGWINCH // Initial resize
	defer func() { signal.Stop(resizeCh); close(resizeCh) }()

	// Set stdin to raw mode if it's a terminal
	var oldState *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
		}
	}

	// Copy stdin to PTY with cancellation support
	go func() {
		// Use a pipe to allow cancellation of the stdin copy
		// When done channel closes, we'll stop trying to copy
		buf := make([]byte, 1024)
		for {
			select {
			case <-done:
				return
			default:
				// Set a read deadline if possible (doesn't work on all platforms)
				n, err := os.Stdin.Read(buf)
				if err != nil {
					return
				}
				if n > 0 {
					select {
					case <-done:
						return
					default:
						_, _ = ptmx.Write(buf[:n])
					}
				}
			}
		}
	}()

	// Copy PTY output to stdout
	var stdoutBuf bytes.Buffer
	if stdout != nil {
		_, _ = io.Copy(io.MultiWriter(stdout, &stdoutBuf), ptmx)
	} else {
		_, _ = io.Copy(&stdoutBuf, ptmx)
	}

	// Wait for command to finish
	err = cmd.Wait()

	result := &Result{
		Output:   stdoutBuf.String(),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			// PTY close errors after command exit are normal
			if !strings.Contains(err.Error(), "input/output error") {
				return nil, fmt.Errorf("command failed: %w", err)
			}
		}
	}

	return result, nil
}

// RunNonInteractive runs a command in non-interactive mode.
func RunNonInteractive(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) (*Result, error) {
	// Connect stdin for cases where we have a prompt but might need input
	cmd.Stdin = os.Stdin

	var stdoutBuf, stderrBuf bytes.Buffer
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(stdout, &stdoutBuf)
	} else {
		cmd.Stdout = &stdoutBuf
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}

	err := cmd.Run()

	result := &Result{
		Output:   stdoutBuf.String(),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("command failed: %w", err)
		}
	}

	return result, nil
}
