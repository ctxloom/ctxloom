//go:build linux

package grpc

import "syscall"

// setRunnerPdeathsig arms PR_SET_PDEATHSIG on a runner subprocess: the kernel
// delivers this signal to the runner the moment its parent (the ctxloom host
// that spawned it) dies, for ANY reason — including the hard kills that never
// give the host a chance to run realLLMConnection.Kill / HostRunner.Kill. See
// isolateRunner for why nothing in userspace can cover that case.
//
// SIGTERM rather than SIGKILL: SIGKILL would reap the runner but strand the
// engine subprocess it had isolated into its own process group one level
// further down. SIGTERM lets InstallRunnerTeardown sweep the runner's whole
// session first. A runner that somehow has no handler installed still dies —
// SIGTERM's default disposition is termination — so this degrades to the
// SIGKILL outcome rather than to the leak.
//
// This mirrors what the integration harness already does one level up
// (tests/integration/testenv/pdeathsig_linux.go, which arms the same signal on
// the ctxloom processes IT spawns); the leak persisted because the harness
// could only protect the hop it owned. Arming it here covers every host —
// `ctxloom run`, `ctxloom mcp`, `ctxloom acp`, a test binary — without each
// having to know about it.
func setRunnerPdeathsig(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}
