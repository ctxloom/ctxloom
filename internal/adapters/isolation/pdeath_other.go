//go:build !linux && !windows

package isolation

import "syscall"

// setRunnerPdeathsig is a no-op outside Linux: syscall.SysProcAttr carries no
// Pdeathsig field on darwin/BSD, which have no PR_SET_PDEATHSIG. The runner
// arms the same guarantee from the inside instead (parentwatch.WithParent, a
// kqueue watch on its parent), which is why it covers our own runner binary
// and cannot cover a foreign one.
func setRunnerPdeathsig(_ *syscall.SysProcAttr) {}
