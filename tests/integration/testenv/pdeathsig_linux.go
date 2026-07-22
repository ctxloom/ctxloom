//go:build linux

package testenv

import "syscall"

// pdeathsigSysProcAttr returns a SysProcAttr that makes a spawned ctxloom
// process receive SIGTERM the instant ITS OWN parent (this test binary)
// terminates — for any reason, including a hard kill of the test binary
// itself. This is the harness-side half of closing the leaked-mock-server
// defect: cmd/run.go already wires signal.NotifyContext(shutdownSignals)
// (SIGINT/SIGTERM/SIGHUP) to unwind through `defer client.Kill()`, which
// reaps the "llm serve <label>" plugin subprocess it spawned (internal/lm/
// grpc's killSession). SIGTERM — not SIGKILL — is deliberate: it is the
// signal that graceful path already listens for, so a test binary dying
// (panic escaping recover, `go test -timeout`'s SIGQUIT, an external `kill
// -9` of the go test process, a deleted worktree that took the shell out
// from under it) still lets the spawned ctxloom run its own cleanup instead
// of vanishing with the plugin child still attached.
//
// PR_SET_PDEATHSIG is Linux-only (see pdeathsig_other.go for the portable
// no-op). It does not, by itself, protect against the spawned ctxloom
// process being killed directly (its own parent — this test binary —
// staying alive): that gap is covered by the pid-precise defense-in-depth
// reap in ptyrun.go's PTYSession.Close and mcpclient.go's MCPClient.Close,
// the two places this harness deliberately hard-kills its own child.
func pdeathsigSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
