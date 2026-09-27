//go:build windows

package tmuxhost

import "errors"

// mkfifo has no Windows form, and there is no tmux to host a window there:
// the refusal is the whole answer.
func mkfifo(string, uint32) error {
	return errors.New("tmuxhost: no named pipe for a launcher's environment on Windows, where tmux does not run")
}
