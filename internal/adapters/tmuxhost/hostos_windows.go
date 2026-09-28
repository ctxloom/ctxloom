//go:build windows

package tmuxhost

import (
	"errors"
	"fmt"
)

// errNoPOSIXHosting is why Windows has no tmux hosting even where an MSYS
// tmux is installed: every launch is a /bin/sh wrapper that sources its
// environment from a named pipe, and Windows has neither.
var errNoPOSIXHosting = errors.New("tmux hosting launches through a POSIX shell and a named pipe, which Windows lacks")

// findTmux refuses on Windows, so NewExecRunner answers ErrTmuxUnavailable
// before any run starts.
func findTmux() (string, error) { return "", errNoPOSIXHosting }

// mkfifo has no Windows form; reaching it means a runner was built around
// NewExecRunner's refusal.
func mkfifo(string, uint32) error {
	return fmt.Errorf("%w: %w", ErrTmuxUnavailable, errNoPOSIXHosting)
}
