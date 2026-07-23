//go:build windows

package pidalive

import "os"

// Alive reports whether pid names a live process. Windows has no signal-0
// probe; FindProcess succeeding is the best cheap approximation, so a stale
// record on Windows errs toward "alive" — every caller's own doc explains
// what that means for its own fallback behavior.
func Alive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
