//go:build !linux && !darwin && !freebsd && !windows

package sessionlock

import "os"

// probeFilesystem has no filesystem probe on this platform, so it cannot
// tell a local disk from a network share — and "cannot determine" is a
// refusal. Every sweep here reports Indeterminate; adding a probe beside the
// sibling files is what lifts that.
func probeFilesystem(dir string) (bool, string, error) {
	if _, err := os.Stat(dir); err != nil {
		return false, "", err
	}
	return false, "unprobed-platform", nil
}
