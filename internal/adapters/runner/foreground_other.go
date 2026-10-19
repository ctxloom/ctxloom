//go:build !linux

package runner

import (
	"os"
	"os/exec"
)

// runOnTerminal runs cmd, whose standard streams are tty, as cmd.Run does.
// Linux's runOnTerminal runs it as the terminal's foreground job instead, and
// a foreground job must be continued when it stops. Only on Linux does Go
// reach waitid, which tells a parent that its child has stopped without
// reaping it if it has exited; without it the stop goes unseen, and a
// foreground job nothing continues would hold the terminal stopped. So here
// cmd stays in the caller's process group, and a ^C typed while its line
// discipline is in effect reaches the caller too.
func runOnTerminal(cmd *exec.Cmd, _ *os.File) error {
	return cmd.Run()
}
