//go:build linux

package hostpty

import "syscall"

// armDeathSignal arms PR_SET_PDEATHSIG on the child: the kernel delivers
// SIGTERM the moment the originator dies for ANY reason — the hard kills
// (SIGKILL, a panic, a torn-down terminal) that never let it reach Kill.
// SIGTERM rather than SIGKILL so the runner's own teardown (its signal
// context) still sweeps the engine it hosts; a runner with no handler dies
// on SIGTERM's default disposition all the same. It fires when the spawning
// THREAD exits, not the process (golang/go#27505), so never Start from a
// LockOSThread'd goroutine that exits still locked.
func armDeathSignal(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGTERM }
