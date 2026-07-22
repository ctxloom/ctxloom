//go:build !linux

package testenv

import "syscall"

// pdeathsigSysProcAttr is a no-op outside Linux: PR_SET_PDEATHSIG has no
// portable equivalent (darwin's syscall.SysProcAttr carries no such field;
// Windows would need a Job Object, out of scope here — see internal/lm/grpc/
// procsession_windows.go for the identical precedent on the production
// side). Non-Linux CI still gets the pid-precise defense-in-depth reap in
// ptyrun.go/mcpclient.go's Close methods, just not the "whole test binary
// hard-killed" case only a kernel-delivered signal can reach — see
// pdeathsig_linux.go.
func pdeathsigSysProcAttr() *syscall.SysProcAttr { return nil }
