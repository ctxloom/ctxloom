//go:build !windows

package coord

import "github.com/ctxloom/ctxloom/internal/shared/pidalive"

// PidAlive reports whether pid names a live process (signal-0 probe; EPERM
// still means alive).
func PidAlive(pid int) bool {
	return pidalive.Alive(pid)
}
