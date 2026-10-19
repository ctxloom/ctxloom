//go:build !windows

package runner

import "os/exec"

// startBatchViaCmdExe leaves cmd as it is: only Windows runs a batch script
// through cmd.exe.
func startBatchViaCmdExe(*exec.Cmd) error { return nil }
