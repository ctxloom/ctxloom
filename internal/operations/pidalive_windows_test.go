//go:build windows

package operations

import "os"

// pidAlive reports whether pid names a live process. Windows has no signal-0
// probe, so FindProcess succeeding is the best cheap approximation; a stale
// sandbox on Windows therefore errs toward "alive" and is left for the
// age-based fallback in reapSandboxes instead.
func pidAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
