//go:build !windows

package tmuxhost

import (
	"golang.org/x/sys/unix"

	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
)

// findTmux resolves the tmux binary through the user's login-shell PATH (see
// shellenv.Resolve for why the process PATH alone is not enough).
func findTmux() (string, error) { return shellenv.Resolve("tmux") }

// mkfifo makes the named pipe a launcher's environment travels through.
func mkfifo(path string, mode uint32) error { return unix.Mkfifo(path, mode) }
