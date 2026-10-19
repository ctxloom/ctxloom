// Package ptyrunner starts a command on a pseudo-terminal: the one translation
// from an os/exec command to a go-pty one, with the platform's adjustments.
package ptyrunner

import (
	"context"
	"os/exec"

	"github.com/aymanbagabas/go-pty"
)

// Command builds go-pty's own Cmd from the caller's *exec.Cmd: the one
// translation from an os/exec command to a pty-started one, shared by every
// party that starts a child on a go-pty pty.
//
// cmd.Args holds argv[0] at index 0. os/exec defaults an empty Args to a
// one-element slice containing Path, but it does so inside exec.Cmd.Start,
// which is never reached from here — the argv is re-derived for go-pty's own
// Cmd instead — so a hand-built &exec.Cmd{Path: ...} arrives with Args nil and
// must not be sliced unconditionally.
//
// argv[0] itself is not ours to set: go-pty's Cmd.start rebuilds the child
// argv as exec.Command(Path, Args[1:]...), so the child always sees Path as
// argv[0] and a caller's own Args[0] cannot reach it through this library.
func Command(ctx context.Context, ptty pty.Pty, cmd *exec.Cmd) *pty.Cmd {
	var args []string
	if len(cmd.Args) > 1 {
		args = cmd.Args[1:]
	}
	c := ptty.CommandContext(ctx, cmd.Path, args...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	// Platform-specific command adjustments (e.g., Windows .cmd/.bat handling)
	adjustPtyCommand(c, cmd)
	return c
}
