//go:build windows

package runner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// startBatchViaCmdExe makes cmd, when it names a batch script, start the
// system's cmd.exe on batchCommandLine's line instead of handing the script
// to CreateProcess, in both launch modes. Any other command is left as it is,
// as is one os/exec could not resolve (cmd.Err), so Start reports that.
func startBatchViaCmdExe(cmd *exec.Cmd) error {
	if cmd.Err != nil || !batchScript(cmd.Path) {
		return nil
	}
	line, err := batchCommandLine(cmd.Path, cmd.Args[1:])
	if err != nil {
		return err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("find cmd.exe for a batch engine: %w", err)
	}
	cmd.Path = filepath.Join(system, "cmd.exe")
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
	return nil
}
