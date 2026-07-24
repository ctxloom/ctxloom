//go:build !linux && !windows

package grpc

import "syscall"

// setRunnerPdeathsig is a no-op outside Linux: PR_SET_PDEATHSIG has no
// portable equivalent and syscall.SysProcAttr carries no Pdeathsig field on
// darwin/BSD. Those platforms keep only the userspace teardown
// (realLLMConnection.Kill / HostRunner.Kill), so a HARD-killed host still
// orphans its runner there — the same honest, documented gap as killSession,
// which needs /proc, and as tests/integration/testenv/pdeathsig_other.go.
// (Windows has its own isolateRunner in procsession_windows.go and never
// reaches this file.)
func setRunnerPdeathsig(_ *syscall.SysProcAttr) {}
