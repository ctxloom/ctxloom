//go:build windows

package grpc

import "os/exec"

// isolateRunner is a no-op on Windows: syscall.SysProcAttr has no Setsid field
// there, and Windows has no POSIX-style session/process-group kill
// primitive — the equivalent would be a Job Object, out of scope here. See
// internal/acp/procgroup_windows.go for the identical precedent (this
// package's killSession is the runner-level counterpart of that file's
// killProcessGroup).
func isolateRunner(_ *exec.Cmd) {}

// killSession is a no-op on Windows for the same reason isolateRunner is: no cheap
// unix-style session kill without Job Objects. A hard-killed host runner's
// orphaned grandchild is today's pre-fix behavior there, same as *nix
// before this file's counterpart existed.
func killSession(_ int) {}

// ReapRunnerDescendants and InstallRunnerTeardown are no-ops on Windows for
// the same reason: there is no session to sweep, and no PR_SET_PDEATHSIG to
// deliver the SIGTERM they would react to. See procsession_unix.go.
func ReapRunnerDescendants() {}

// InstallRunnerTeardown is a no-op on Windows — see ReapRunnerDescendants.
func InstallRunnerTeardown() {}
