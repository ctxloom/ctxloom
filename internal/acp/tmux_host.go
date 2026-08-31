package acp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Interactive HOSTING, as distinct from the terminal/* capture path in
// tmux_terminal.go. Both put a command in a tmux window; they differ in what
// the command's stdout IS, and that difference is the entire feature.
//
//	capture (tmuxOutputWrapper)  stdout is a FILE   -> pane blank, no stdin,
//	                                                   an interactive program
//	                                                   sees "not a terminal"
//	hosting (this file)          stdout is the PTY  -> pane renders live, a
//	                                                   human can attach, and
//	                                                   output is captured by
//	                                                   tmux pipe-pane instead
//
// Capture is right for terminal/*: an engine asks for a command's bytes and
// exit code and nobody watches it run. Hosting is what lets a real UI live in
// the multiplexer, which is what the capture wrapper fundamentally cannot do —
// redirecting stdout to a file is not a limitation to work around, it is the
// contract that path implements.
//
// WHY pipe-pane AND NOT capture-pane. The top-of-file note in
// tmux_terminal.go rejects capture-pane, correctly: once a pane dies tmux
// overwrites its scrollback with a synthetic "Pane is dead (status N, ...)"
// line, so the real output would have to be scraped out from under it. That
// objection is about SCRAPING SCROLLBACK AFTER THE FACT. pipe-pane is a
// different mechanism — it streams the pane's bytes to a command as they are
// produced — so the objection does not reach it, and hosting can have a live
// pane and a captured copy at the same time.
//
// THE COST, stated because it is a real difference and not a detail: pipe-pane
// captures the pane's RENDERED byte stream, so output carries whatever escape
// sequences the program emitted. That is correct for hosting (a UI's output is
// its escapes) and is why terminal/* is deliberately left on the capture path,
// whose consumers expect clean bytes.

// hostSpec describes one interactively hosted command.
type hostSpec struct {
	Command string
	Args    []string
	Cwd     string            // empty means tmux's own default
	Env     map[string]string // passed via `new-window -e`
}

// The wrapper both surfaces share lives in tmux_terminal.go
// (tmuxWindowWrapper); hosting selects it with writerPipePane.

// socketName reports the tmux server this registry's runner talks to. The
// wrapper script above has to name the socket INSIDE a command string, so it
// cannot rely on the runner prepending `-L`. Resolved by assertion rather than
// widened onto the tmuxRunner interface because only a real runner ever
// executes a wrapper: the unit-test fake records argv and runs nothing, so
// requiring it to answer this would be ceremony with no caller.
func (l *localTerminals) socketName() string {
	if s, ok := l.runner.(interface{ socketName() string }); ok {
		return s.socketName()
	}
	return tmuxSocketName
}

// host starts spec's command in a tmux window on a real pty and begins
// capturing the pane. The command does not run until capture is armed.
func (l *localTerminals) host(ctx context.Context, spec hostSpec) (*tmuxTerminal, error) {
	if spec.Command == "" {
		return nil, fmt.Errorf("host: no command given")
	}
	if err := l.ensureSession(ctx); err != nil {
		return nil, err
	}

	n := l.seq.Add(1)
	name := fmt.Sprintf("%s-h%d", l.run, n)
	// The SAME handle type terminal/* uses. tmuxTerminal.window is
	// "<session>:<window>", which is exactly the target a human passes to
	// `tmux -L <socket> attach -t ...` -- so hosting needs no handle of its own,
	// and the four lifecycle operations below are the shared ones.
	h := &tmuxTerminal{
		window: tmuxSessionName + ":" + name, channel: "ctxloom-acp-host-" + name,
		outputPath: filepath.Join(l.tmpDir, "ctxloom-acp-host-"+name+".out"),
		statusPath: filepath.Join(l.tmpDir, "ctxloom-acp-host-"+name+".status"),
	}
	gate := h.channel + "-gate"

	args := []string{"new-window", "-d", "-t", tmuxSessionName, "-n", name}
	if spec.Cwd != "" {
		args = append(args, "-c", spec.Cwd)
	}
	// Sorted so the argv is deterministic — an unordered map would make the
	// command line differ run to run for identical input, which turns any
	// future argv assertion into a flake.
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	// writerPipePane: the command's stdout is the window's real PTY, so an
	// interactive program behaves as it would in any terminal, and pipe-pane
	// (armed below, before the gate is released) copies the bytes out.
	args = append(args, "sh", "-c", tmuxWindowWrapper,
		l.socketName(), string(writerPipePane), h.outputPath, gate, h.statusPath, h.channel, spec.Command)
	args = append(args, spec.Args...)

	if _, err := l.runner.Run(ctx, args...); err != nil {
		return nil, fmt.Errorf("host %q: %w", spec.Command, err)
	}

	// Arm capture BEFORE releasing the gate. pipe-pane attaches to a live
	// pane; if the command had already run and exited, tmux answers "target
	// pane has exited" and nothing is ever captured.
	if _, err := l.runner.Run(ctx, "pipe-pane", "-o", "-t", h.window,
		"cat >> "+shellQuote(h.outputPath)); err != nil {
		return nil, fmt.Errorf("host %q: arm capture: %w", spec.Command, err)
	}
	if _, err := l.runner.Run(ctx, "wait-for", "-S", gate); err != nil {
		return nil, fmt.Errorf("host %q: release gate: %w", spec.Command, err)
	}

	// Reclaim the terminal when it stops being needed, which is when the
	// session context ends. This is an explicit REGISTRATION against session
	// lifetime, deliberately not a defer: a defer fires when its own call
	// frame returns, which for a backgrounded terminal is far too early, and
	// it would not fire at all if the session ended by some path that does
	// not run through this frame.
	//
	// The hook necessarily runs with ctx ALREADY cancelled, so it must not
	// reuse ctx for its own tmux calls — execTmuxRunner builds an
	// exec.CommandContext, and a cancelled context kills the command before
	// it can run. WithoutCancel keeps the values and drops the cancellation.
	h.stop = context.AfterFunc(ctx, func() {
		l.releaseWindow(context.WithoutCancel(ctx), h)
	})
	return h, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// shellQuote makes a path safe inside pipe-pane's shell command string.
// pipe-pane takes a COMMAND, not an argv, so a path containing a space or a
// quote would otherwise split or escape.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
