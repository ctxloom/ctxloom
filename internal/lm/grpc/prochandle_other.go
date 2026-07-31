//go:build !windows && !linux

package grpc

import "syscall"

// procHandle is the non-Linux stand-in for the pidfd-pinned handle (see
// prochandle_linux.go). There is no portable way to pin a process's identity
// here, so this keeps the pid and signals it directly — the pre-existing
// behaviour, and the same honest platform gap that killSession's /proc
// enumeration already has (it only works on Linux at all).
type procHandle struct{ pid int }

func pinProcess(pid int) (procHandle, bool) { return procHandle{pid: pid}, true }

func (h procHandle) kill() { _ = syscall.Kill(h.pid, syscall.SIGKILL) }

func (h procHandle) close() {}
