//go:build linux

package hostpty

import "syscall"

// armDeathSignal arms PR_SET_PDEATHSIG on the child: the kernel delivers
// SIGTERM the moment the originator dies for ANY reason — the hard kills
// (SIGKILL, a panic, a torn-down terminal) that never let it reach Kill.
// SIGTERM rather than SIGKILL so the runner's own teardown (its signal
// context) still sweeps the engine it hosts; a runner with no handler dies
// on SIGTERM's default disposition all the same.
func armDeathSignal(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGTERM }
