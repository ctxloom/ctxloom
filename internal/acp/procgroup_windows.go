//go:build windows

package acp

import "os/exec"

// setpgid is a no-op on Windows: syscall.SysProcAttr has no Setpgid field
// there (it is an entirely different struct shape), and Windows has no
// POSIX-style process-group kill primitive — the equivalent would be a Job
// Object, which is out of scope here (codex-acp's leaked worker is a *nix
// double-fork; this ACP transport is otherwise cross-platform, so it still
// builds and runs on Windows with today's single-process kill behavior).
func setpgid(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing just the immediate process — today's
// pre-fix behavior — since there is no cheap unix-style process-group kill on
// Windows without Job Objects.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
