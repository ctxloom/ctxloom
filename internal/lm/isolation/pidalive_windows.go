//go:build windows

package isolation

import "github.com/ctxloom/ctxloom/internal/shared/pidalive"

// pidAlive reports whether pid names a live process — see
// internal/shared/pidalive's doc for why this probe lives in its own
// dependency-free leaf package rather than being duplicated.
func pidAlive(pid int) bool {
	return pidalive.Alive(pid)
}
