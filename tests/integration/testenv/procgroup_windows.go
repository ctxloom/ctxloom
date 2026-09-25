//go:build windows

package testenv

import (
	"os"
	"os/exec"
)

// ownProcessGroup is a no-op on Windows: there is no POSIX process group to
// join, and the equivalent (a Job Object) is out of scope here for the same
// reason as reap_windows.go's no-ops.
func ownProcessGroup(*exec.Cmd) {}

// killProcessGroup kills only p itself on Windows — the no-group variant.
// A descendant still holding the output pipes is then cut loose by
// boundedRun's orphanGrace instead of by the kill.
func killProcessGroup(p *os.Process) {
	_ = p.Kill()
}
