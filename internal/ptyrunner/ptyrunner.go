// Package ptyrunner provides cross-platform PTY support for running interactive commands.
package ptyrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

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

	// Set stdin to raw mode if it's a terminal
	var oldState *term.State
	stdinIsTerm := term.IsTerminal(int(os.Stdin.Fd()))
	if stdinIsTerm {
		oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer func() {
				_ = term.Restore(int(os.Stdin.Fd()), oldState)
				// Reset terminal to sane state after subprocess exits.
				// term.State doesn't capture all attributes (like ONLCR),
				// so we use stty sane to fix any line discipline corruption.
				if stdinIsTerm {
					cmd := exec.Command("stty", "sane")
					cmd.Stdin = os.Stdin
					_ = cmd.Run()
				}
			}()
		}
	}

	// Copy stdin to PTY with cancellation support
	go func() {
		buf := make([]byte, 1024)
		for {
			select {
			case <-done:
				return
			default:
				n, err := os.Stdin.Read(buf)
				if err != nil {
					return
				}
				if n > 0 {
					select {
					case <-done:
						return
					default:
						_, _ = ptty.Write(buf[:n])
					}
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
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			// PTY close errors after command exit are normal
			if !strings.Contains(err.Error(), "input/output error") &&
				!strings.Contains(err.Error(), "file already closed") {
				return nil, fmt.Errorf("command failed: %w", err)
			}
		}
	}

	return result, nil
}

