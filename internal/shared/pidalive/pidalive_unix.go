//go:build !windows

// Package pidalive provides ONE shared liveness probe for a bare pid,
// consolidating what used to be three near-identical copies
// (agentcoord/coord.PidAlive, internal/operations' test-only pidAlive, and
// internal/lm/isolation's startup-reaper pidAlive) into a single leaf package
// with no internal dependencies — safe for any package to import without
// creating a cycle.
package pidalive

import (
	"os"
	"syscall"
)

// Alive reports whether pid names a live process (signal-0 probe; no signal
// is actually delivered). EPERM still means alive — a live process this user
// doesn't own is still alive; ESRCH (or any other error) means gone.
func Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
