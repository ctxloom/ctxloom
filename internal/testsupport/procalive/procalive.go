//go:build !windows

// Package procalive is a TEST-ONLY zombie-aware liveness check: it answers
// whether pid names a process that is both present in the process table and
// not a zombie.
//
// It exists because syscall.Kill(pid, 0) alone SUCCEEDS against a zombie: a
// child that has genuinely exited but has not yet been reaped by its parent
// still answers kill(pid, 0) with no error. A reap regression test built on
// that check alone cannot distinguish "died and was not reaped" from "never
// died" — it reads green wherever something (an init, a shell, a debugger)
// happens to reap zombies promptly, and red in any PID namespace whose PID 1
// does not reap, which includes every agent container cell whose PID 1 is one
// of ctxloom's own processes rather than a real init. That makes the test's
// verdict a property of the environment, not of the subject under test.
//
// This is deliberately separate from internal/shared/pidalive: that package
// is production code with its own pid-reuse caveats and a documented
// MaybeAlive policy several real callers (agentcoord, operations, isolation)
// already depend on — widening its contract to special-case zombies would be
// a production behavior change nobody asked for. This package instead
// answers a narrower, test-only question for tests that spawn a child
// themselves and watch it through its own lifetime, where pid reuse is not a
// concern.
package procalive

import (
	"syscall"

	"github.com/ctxloom/ctxloom/internal/shared/procpin"
)

// Alive reports whether pid names a still-running, non-zombie process.
func Alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	return !isZombie(pid)
}

// isZombie reports whether procpin.ReadStat marks pid state Z.
//
// A read failure (no /proc — ReadStat's ErrUnsupported off Linux — or the pid
// is already gone) is treated as "cannot tell, so not a zombie" rather than an
// error: Alive's kill(pid,0) check above is what decides existence; this only
// narrows an already-confirmed-present pid.
func isZombie(pid int) bool {
	st, err := procpin.ReadStat(pid)
	return err == nil && st.State == 'Z'
}
