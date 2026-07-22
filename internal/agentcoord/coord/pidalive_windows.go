//go:build windows

package coord

import "os"

// PidAlive reports whether pid names a live process. Windows has no signal-0
// probe; FindProcess succeeding is the best cheap approximation, so a stale
// lock on Windows errs toward "alive" (the claimant then takes the ephemeral
// fallback — never a shared journal).
func PidAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
