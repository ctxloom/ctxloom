//go:build windows

package ptyrunner

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

// isBenignPTYError reports whether err from c.Wait is the expected fallout of
// closing the ConPTY after the command already exited. Windows has no EIO
// equivalent for this path; a closed handle surfaces as fs.ErrClosed. Matched
// by sentinel via errors.Is — never by substring of the error text.
func isBenignPTYError(err error) bool {
	return errors.Is(err, fs.ErrClosed)
}

// adjustPtyCommand handles Windows-specific .cmd/.bat file execution via ConPTY.
//
// Windows CreateProcess cannot properly execute .cmd/.bat files when the path
// contains spaces. It internally invokes cmd.exe /c, but cmd.exe's
// quote-stripping behavior (condition 2 in the /c docs) breaks quoted paths
// when additional quoted arguments are present.
//
// Fix: explicitly invoke cmd.exe with a carefully quoted /c command line.
// The entire inner command is wrapped in an extra pair of quotes so that
// cmd.exe's quote-stripping leaves the inner quoting intact.
func adjustPtyCommand(c *pty.Cmd, original *exec.Cmd) {
	ext := strings.ToLower(filepath.Ext(original.Path))
	if ext != ".cmd" && ext != ".bat" {
		return
	}

	// Build the inner command line with proper quoting via ComposeCommandLine.
	// original.Args[0] is the resolved .cmd path; the rest are arguments.
	innerCmdLine := windows.ComposeCommandLine(original.Args)

	// Wrap with cmd.exe /c "..inner.." — the outer quotes survive cmd.exe's
	// condition-2 stripping, preserving all inner quoting.
	c.Path = "cmd.exe"
	c.Args = []string{"cmd.exe"}
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CmdLine = `/c "` + innerCmdLine + `"`
}
