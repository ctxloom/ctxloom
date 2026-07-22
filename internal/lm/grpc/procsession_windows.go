//go:build windows

package grpc

import "os/exec"

// setsid is a no-op on Windows: syscall.SysProcAttr has no Setsid field
// there, and Windows has no POSIX-style session/process-group kill
// primitive — the equivalent would be a Job Object, out of scope here. See
// internal/acp/procgroup_windows.go for the identical precedent (this
// package's killSession is the runner-level counterpart of that file's
// killProcessGroup).
func setsid(_ *exec.Cmd) {}

// killSession is a no-op on Windows for the same reason setsid is: no cheap
// unix-style session kill without Job Objects. A hard-killed host runner's
// orphaned grandchild is today's pre-fix behavior there, same as *nix
// before this file's counterpart existed.
func killSession(_ int) {}
