package tmuxhost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// writeLauncher writes a small shell script carrying the launch's ENVIRONMENT
// and ARGV, and returns the two argv words that run it.
//
// It exists because `tmux new-window`'s command line is not an ordinary argv:
// the client packs it into a fixed-size buffer and refuses anything longer with
// "command too long". Spelling the launch inline made that line grow with two
// unbounded inputs at once — one `-e` per environment variable (a desktop
// session carries a hundred), and the command's own arguments, which for an
// engine include a multi-kilobyte prompt. Past the limit the window is never
// created and the whole session dies, naming neither the variable nor the
// argument responsible.
//
// Routing both through a file makes the command line CONSTANT-SIZE, so no
// input can overflow it. The environment is exported rather than passed as
// `-e` because the two are equivalent where it counts — the hosted process
// inherits it either way — and nothing reads the tmux window environment back.
func writeLauncher(dir, name string, env map[string]string, command string, args []string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("launcher: no command given")
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	// Sorted so the file is deterministic — an unordered map would rewrite it
	// differently run to run for identical input, which turns any future
	// content assertion into a flake. (The same reason the -e loop sorted.)
	for _, k := range collections.SortedKeys(env) {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	// exec, so the hosted program REPLACES this shell: the wrapper waits on
	// the process it started and reports its exit status, and an extra shell
	// in between would report its own.
	b.WriteString("exec " + shellQuote(command))
	for _, a := range args {
		b.WriteString(" " + shellQuote(a))
	}
	b.WriteString("\n")

	path := filepath.Join(dir, "ctxloom-launch-"+name+".sh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("launcher: create %s: %w", dir, err)
	}
	// 0o700: the wrapper execs this file, so it must carry the exec bit, and
	// nothing but its owner has any business running it.
	if err := iox.WriteFileAtomic(path, []byte(b.String()), 0o700); err != nil {
		return "", fmt.Errorf("launcher: write %s: %w", path, err)
	}
	return path, nil
}
