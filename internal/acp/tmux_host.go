package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// hostedTerminal is a handle to a hosted command.
type hostedTerminal struct {
	// AttachTarget is "<session>:<window>", the target a human passes to
	// `tmux -L <socket> attach -t ...` (or select-window) to watch this run.
	// It is the whole point of hosting, so it is exported on the handle
	// rather than reconstructed by callers.
	AttachTarget string

	channel    string
	outputPath string
	statusPath string
}

// hostedStatus is a finished hosted command's outcome.
type hostedStatus struct {
	ExitCode int
}

// tmuxHostWrapper runs the command as a CHILD rather than exec'ing it, which
// is what makes both halves of the contract possible at once:
//
//   - no redirection, so the child inherits the window's pty and an
//     interactive program behaves as it would in any terminal;
//   - the shell survives the child, so it can record the true exit code and
//     signal the wait channel afterwards. `exec "$@"` would replace the shell
//     and leave nothing to do either.
//
// The leading `wait-for` is a STARTING GATE, not a delay: it blocks until
// host() has armed pipe-pane, so no output can be produced before capture is
// attached. tmux's wait-for is a counting channel, so a signal that arrives
// first is remembered rather than lost — the gate is therefore race-free in
// both directions, and needs no sleep or poll.
//
// Invoked as: sh -c <wrapper> _ <gate> <statusPath> <channel> <command> [args...]
const tmuxHostWrapper = `tmux -L "$0" wait-for "$1"
shift
statusfile="$1"
shift
ch="$1"
shift
"$@"
ec=$?
echo "$ec" > "$statusfile"
tmux -L "$0" wait-for -S "$ch"
exit $ec`

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
func (l *localTerminals) host(ctx context.Context, spec hostSpec) (hostedTerminal, error) {
	if spec.Command == "" {
		return hostedTerminal{}, fmt.Errorf("host: no command given")
	}
	if err := l.ensureSession(ctx); err != nil {
		return hostedTerminal{}, err
	}

	n := l.seq.Add(1)
	name := fmt.Sprintf("%s-h%d", l.run, n)
	h := hostedTerminal{
		AttachTarget: tmuxSessionName + ":" + name,
		channel:      "ctxloom-acp-host-" + name,
		outputPath:   filepath.Join(l.tmpDir, "ctxloom-acp-host-"+name+".out"),
		statusPath:   filepath.Join(l.tmpDir, "ctxloom-acp-host-"+name+".status"),
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
	args = append(args, "sh", "-c", tmuxHostWrapper,
		l.socketName(), gate, h.statusPath, h.channel, spec.Command)
	args = append(args, spec.Args...)

	if _, err := l.runner.Run(ctx, args...); err != nil {
		return hostedTerminal{}, fmt.Errorf("host %q: %w", spec.Command, err)
	}

	// Arm capture BEFORE releasing the gate. pipe-pane attaches to a live
	// pane; if the command had already run and exited, tmux answers "target
	// pane has exited" and nothing is ever captured.
	if _, err := l.runner.Run(ctx, "pipe-pane", "-o", "-t", h.AttachTarget,
		"cat >> "+shellQuote(h.outputPath)); err != nil {
		return hostedTerminal{}, fmt.Errorf("host %q: arm capture: %w", spec.Command, err)
	}
	if _, err := l.runner.Run(ctx, "wait-for", "-S", gate); err != nil {
		return hostedTerminal{}, fmt.Errorf("host %q: release gate: %w", spec.Command, err)
	}
	return h, nil
}

// waitHosted blocks until the hosted command exits and reports its status.
func (l *localTerminals) waitHosted(ctx context.Context, h hostedTerminal) (*hostedStatus, error) {
	if _, err := l.runner.Run(ctx, "wait-for", h.channel); err != nil {
		return nil, fmt.Errorf("wait for hosted terminal: %w", err)
	}
	raw, err := os.ReadFile(h.statusPath)
	if err != nil {
		return nil, fmt.Errorf("read hosted exit status: %w", err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("parse hosted exit status %q: %w", strings.TrimSpace(string(raw)), err)
	}
	return &hostedStatus{ExitCode: code}, nil
}

// hostedOutput returns what the pane has produced so far. Readable while the
// command is still running — pipe-pane streams rather than writing at exit.
func (l *localTerminals) hostedOutput(h hostedTerminal) (string, error) {
	b, err := os.ReadFile(h.outputPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing captured yet is not an error: a hosted UI may not have
			// drawn anything at the moment a caller looks.
			return "", nil
		}
		return "", fmt.Errorf("read hosted output: %w", err)
	}
	return string(b), nil
}

// killHosted destroys the window and unblocks anyone in waitHosted. The
// wrapper never reaches its own signal line when the window is killed out from
// under it, so the signal is sent here unconditionally.
func (l *localTerminals) killHosted(ctx context.Context, h hostedTerminal) error {
	_, err := l.runner.Run(ctx, "kill-window", "-t", h.AttachTarget)
	_, _ = l.runner.Run(ctx, "wait-for", "-S", h.channel)
	if err != nil {
		return fmt.Errorf("kill hosted terminal: %w", err)
	}
	return nil
}

// releaseHosted frees everything a hosted terminal owns: the tmux window and
// the two files behind it. This is the counterpart to terminal/*'s release()
// and exists for the same reason — the capture and status files are a
// transient MAILBOX, not a log. They exist because ctxloom is not the parent
// of the process tmux spawns and so has no fd to inherit, and they must
// outlive the pane because a dead pane's scrollback is replaced by tmux's own
// "Pane is dead (status N, ...)" placeholder. Once the handle is released
// nothing reads them again, so anything left on disk is a leak.
//
// kill-window runs UNCONDITIONALLY, matching release(): under remain-on-exit a
// naturally-exited command leaves its dead pane and window in the session
// forever otherwise.
//
// Kill and release stay SEPARATE, also matching the terminal/* path: after a
// kill the exit status is still readable, and collapsing the two would destroy
// it before a caller could ask.
func (l *localTerminals) releaseHosted(ctx context.Context, h hostedTerminal) error {
	_, _ = l.runner.Run(ctx, "kill-window", "-t", h.AttachTarget)
	_, _ = l.runner.Run(ctx, "wait-for", "-S", h.channel)
	_ = os.Remove(h.outputPath)
	_ = os.Remove(h.statusPath)
	return nil
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
