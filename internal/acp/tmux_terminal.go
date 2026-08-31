package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	api "github.com/coder/acp-go-sdk"
)

// This file backs handleTerminal's LOCAL path (see session.go): when
// acp_local_terminal is on and no upstream editor advertised the terminal
// capability to forward to, ctxloom serves ACP's terminal/* itself, mapping
// each method onto tmux(1):
//
//	CreateTerminal        -> tmux new-window
//	TerminalOutput        -> read a captured-output file (see below, not
//	                          tmux capture-pane)
//	WaitForTerminalExit   -> tmux wait-for, blocking on a per-terminal channel
//	KillTerminal          -> tmux kill-window
//	ReleaseTerminal       -> drop the handle (killing it first if still running)
//
// TerminalOutput does not read tmux's own pane scrollback: once a pane dies,
// tmux overwrites what capture-pane would return with its own "Pane is dead
// (status N, ...)" placeholder text, so the real output would have to be
// scraped back out from underneath that synthetic line. Instead the
// spawned command's stdout/stderr are redirected straight to a plain file by
// a small shell wrapper, and TerminalOutput just reads that file — content
// survives KillTerminal (which destroys the tmux window outright, not just
// marks it dead) for exactly the same reason.

// tmuxSocketName names the DEDICATED tmux server this file talks to
// (`tmux -L tmuxSocketName ...`) — never the caller's own default tmux
// server. ensureSession sets a server-wide default (ensureSession's own
// doc); doing that against a user's real tmux server would leak into every
// session they run interactively. A throwaway server on its own socket is
// the isolation boundary that makes that safe.
const tmuxSocketName = "ctxloom-acp-terminal"

// tmuxSessionName is the one fixed session every local terminal's window
// lives in, created if missing on first use.
//
// UNRESOLVED BY THE ACP SPEC: nothing says who owns the session a
// `tmux new-window` target requires to already exist. A fixed,
// create-if-missing name is the smallest defensible choice here — the
// rejected alternative was a session named per chatSession (per ACP
// session id), which would have needed its own teardown path (who deletes
// it, and when, given a session can outlive the terminals opened in it) for
// no isolation benefit: window names are already minted uniquely per
// terminal (localTerminals.seq), so concurrent chatSessions sharing one
// tmux session cannot collide on a window target either way.
const tmuxSessionName = "ctxloom-acp-terminal"

// tmuxRunner executes one tmux subcommand against the dedicated ctxloom
// socket and returns its stdout. Abstracted so unit tests can drive the
// mapping logic (localTerminals) without a real tmux binary; production
// code always wires execTmuxRunner.
type tmuxRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// execTmuxRunner shells out to the real tmux binary on tmuxSocketName. A
// missing tmux binary surfaces as an ordinary *exec.Error from cmd.Run,
// which callers turn into a fail-loud, remedy-carrying jsonrpc.Error
// (localTerminalError) rather than a silent decline — see the config flag's
// own doc (config.Config's acpLocalTerminal field) for why that is the
// required behaviour, not a preference.
type execTmuxRunner struct {
	// bin overrides the tmux binary path; empty means "tmux" (resolved via
	// PATH). Test-only seam.
	bin string
}

func (r execTmuxRunner) Run(ctx context.Context, args ...string) (string, error) {
	bin := r.bin
	if bin == "" {
		bin = "tmux"
	}
	full := append([]string{"-L", tmuxSocketName}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	var out, stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// tmuxTerminal is one open local terminal: the tmux window backing it, the
// wait-for(1) channel its wrapper script signals on exit, and the two files
// that carry its output and exit code independently of the window's own
// lifetime (see this file's top-of-file doc for why).
type tmuxTerminal struct {
	window     string // tmux target: "<tmuxSessionName>:<name>"
	channel    string
	outputPath string
	statusPath string
	limit      *int // CreateTerminalRequest.OutputByteLimit, retained for every later TerminalOutput call

	// mu guards exitStatus/killed, which a WaitForTerminalExit call (reading)
	// and a KillTerminal call (writing) can reach concurrently — see kill's
	// own comment for the race this closes.
	mu         sync.Mutex
	exitStatus *api.TerminalExitStatus // set once known: from the status file, or synthesized by kill
	killed     bool
}

// localTerminals is the per-chatSession registry of open local terminals.
// nil on a chatSession unless acp_local_terminal is on for it (see
// session.go's Chat).
type localTerminals struct {
	runner tmuxRunner
	tmpDir string // os.TempDir() in production; a t.TempDir() in tests

	mu      sync.Mutex
	ensured bool
	seq     atomic.Uint64
	// run distinguishes this process's terminals from those of any other run
	// sharing the fixed tmux server — see newLocalTerminals.
	run   string
	terms map[api.TerminalId]*tmuxTerminal
}

func newLocalTerminals(runner tmuxRunner, tmpDir string) *localTerminals {
	return &localTerminals{
		runner: runner, tmpDir: tmpDir,
		terms: map[api.TerminalId]*tmuxTerminal{},
		// run is what keeps two ctxloom processes sharing this server from
		// minting the same window name. seq alone cannot: it is per-process
		// and restarts at zero, so every run's first terminal would be "t1".
		// tmux ALLOWS duplicate window names, so a collision does not error —
		// it SHADOWS the older window, leaving kill-window and the wait target
		// ambiguous and blocking the newer run forever on a channel its own
		// window never signals. Measured as a 30-minute hang before this.
		run: runToken(),
	}
}

// runToken mints a short token unique to this localTerminals. Uniqueness is
// all that is required — not unpredictability — so a rand failure falls back
// to the clock rather than failing terminal creation.
func runToken() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// ensureSession makes sure tmuxSessionName exists on the dedicated socket,
// creating it (and, ONCE, this dedicated server's remain-on-exit default)
// if this is the first local terminal any chatSession in this process has
// opened.
//
// remain-on-exit is set with set-option -g (server-wide), not per-window:
// tmux only applies a window-option override set via `set-option -t
// <session>` to windows that already exist at the moment it runs, not to
// ones created afterward (measured — a per-window `set-option -t
// <session>:<name>` set right after `new-window` loses the race against a
// fast-exiting command every time). A server-wide default is safe here
// specifically because tmuxSocketName names a throwaway server nothing else
// ever attaches to; the same call against a real interactive tmux server
// would change every window in every session running on it.
//
// remain-on-exit itself is not load-bearing for TerminalOutput (which reads
// a file, not tmux's own scrollback — see the top-of-file doc) but it keeps
// a just-exited window's WAIT_TIMEOUT-vulnerable state inspectable via tmux
// directly during debugging, and costs nothing to set once.
func (l *localTerminals) ensureSession(ctx context.Context) error {
	l.mu.Lock()
	already := l.ensured
	l.mu.Unlock()
	if already {
		return nil
	}
	if _, err := l.runner.Run(ctx, "has-session", "-t", tmuxSessionName); err != nil {
		if _, err := l.runner.Run(ctx, "new-session", "-d", "-s", tmuxSessionName); err != nil {
			// Tolerate a race against another ctxloom process creating the
			// same fixed session at the same moment, rather than failing a
			// terminal/create that would otherwise have worked fine.
			if _, herr := l.runner.Run(ctx, "has-session", "-t", tmuxSessionName); herr != nil {
				return fmt.Errorf("create tmux session %q: %w", tmuxSessionName, err)
			}
		}
	}
	// Configure UNCONDITIONALLY, not only on the create path. The session
	// routinely outlives the process that made it (nothing tears this server
	// down), so adopting an existing one is the normal case, not the exotic
	// one — and an adopted server that skipped this would behave differently
	// from a freshly created one for the rest of its life.
	if _, err := l.runner.Run(ctx, "set-option", "-g", "remain-on-exit", "on"); err != nil {
		return fmt.Errorf("configure tmux session %q: %w", tmuxSessionName, err)
	}
	l.mu.Lock()
	l.ensured = true
	l.mu.Unlock()
	return nil
}

// tmuxOutputWrapper is the shell script every local terminal's window runs
// instead of the caller's command directly. Invoked as
// `sh -c tmuxOutputWrapper _ <outputPath> <statusPath> <channel> <command> [args...]`,
// so inside the script $1=outputPath, $2=statusPath, $3=channel, and "$@"
// (after the two shifts) is the command and its arguments — run via `exec
// "$@"`, never through a nested shell, so an argument containing shell
// metacharacters is passed through literally rather than re-interpreted.
//
// It redirects the command's own stdout/stderr straight to outputPath (this
// file, not tmux's pane scrollback, is what TerminalOutput reads — see the
// top-of-file doc), writes the exit code to statusPath, and signals channel
// last so a blocked `tmux wait-for` is guaranteed to see the status file
// already written.
const tmuxOutputWrapper = `exec > "$1" 2>&1
shift
statusfile="$1"
shift
ch="$1"
shift
"$@"
ec=$?
echo "$ec" > "$statusfile"
tmux -L ` + tmuxSocketName + ` wait-for -S "$ch"
exit $ec`

// create opens a new local terminal for req, returning the id every later
// call (output/wait/kill/release) addresses it by.
func (l *localTerminals) create(ctx context.Context, req api.CreateTerminalRequest) (api.CreateTerminalResponse, error) {
	if err := l.ensureSession(ctx); err != nil {
		return api.CreateTerminalResponse{}, err
	}
	n := l.seq.Add(1)
	name := fmt.Sprintf("%s-t%d", l.run, n)
	id := api.TerminalId(name)
	outputPath := filepath.Join(l.tmpDir, "ctxloom-acp-term-"+name+".out")
	statusPath := filepath.Join(l.tmpDir, "ctxloom-acp-term-"+name+".status")
	channel := "ctxloom-acp-term-" + name

	args := []string{"new-window", "-d", "-t", tmuxSessionName, "-n", name}
	if req.Cwd != nil && *req.Cwd != "" {
		args = append(args, "-c", *req.Cwd)
	}
	for _, e := range req.Env {
		args = append(args, "-e", e.Name+"="+e.Value)
	}
	args = append(args, "sh", "-c", tmuxOutputWrapper, "_", outputPath, statusPath, channel, req.Command)
	args = append(args, req.Args...)

	if _, err := l.runner.Run(ctx, args...); err != nil {
		return api.CreateTerminalResponse{}, err
	}

	term := &tmuxTerminal{
		window:     tmuxSessionName + ":" + name,
		channel:    channel,
		outputPath: outputPath,
		statusPath: statusPath,
		limit:      req.OutputByteLimit,
	}
	l.mu.Lock()
	l.terms[id] = term
	l.mu.Unlock()
	return api.CreateTerminalResponse{TerminalId: id}, nil
}

func (l *localTerminals) lookup(id api.TerminalId) (*tmuxTerminal, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.terms[id]
	return t, ok
}

// readStatus returns t's exit status once known: the cached value if
// kill or an earlier read already resolved it, else whatever the status
// file currently holds (nil if the command has not exited yet).
func readStatus(t *tmuxTerminal) *api.TerminalExitStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exitStatus != nil {
		return t.exitStatus
	}
	b, err := os.ReadFile(t.statusPath)
	if err != nil {
		return nil
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	t.exitStatus = &api.TerminalExitStatus{ExitCode: &code}
	return t.exitStatus
}

// truncateFromStart keeps at most limit bytes of s, dropping from the
// BEGINNING as CreateTerminalRequest.OutputByteLimit's doc requires, and
// advances the cut point forward to the next UTF-8 rune boundary so the
// retained string is never split mid-character.
func truncateFromStart(s string, limit int) (string, bool) {
	if limit < 0 || len(s) <= limit {
		return s, false
	}
	cut := len(s) - limit
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:], true
}

// output answers terminal/output: the captured file content (see
// tmuxOutputWrapper's doc), trimmed to the terminal's OutputByteLimit if one
// was given at create time, plus the exit status if the command has
// finished.
func (l *localTerminals) output(_ context.Context, req api.TerminalOutputRequest) (api.TerminalOutputResponse, error) {
	t, ok := l.lookup(req.TerminalId)
	if !ok {
		return api.TerminalOutputResponse{}, fmt.Errorf("unknown terminal %q", req.TerminalId)
	}
	content, err := os.ReadFile(t.outputPath)
	if err != nil && !os.IsNotExist(err) {
		return api.TerminalOutputResponse{}, fmt.Errorf("read terminal output: %w", err)
	}
	out := string(content)
	truncated := false
	if t.limit != nil {
		out, truncated = truncateFromStart(out, *t.limit)
	}
	return api.TerminalOutputResponse{Output: out, Truncated: truncated, ExitStatus: readStatus(t)}, nil
}

// wait answers terminal/wait_for_exit: blocks (bounded by ctx) on the
// terminal's tmux wait-for channel, then returns its exit status. Safe to
// call after the command has already exited — tmux wait-for is a counting
// semaphore, so a channel already signalled (by the wrapper script, or by
// kill below) returns immediately rather than hanging.
func (l *localTerminals) wait(ctx context.Context, req api.WaitForTerminalExitRequest) (api.WaitForTerminalExitResponse, error) {
	t, ok := l.lookup(req.TerminalId)
	if !ok {
		return api.WaitForTerminalExitResponse{}, fmt.Errorf("unknown terminal %q", req.TerminalId)
	}
	if _, err := l.runner.Run(ctx, "wait-for", t.channel); err != nil {
		return api.WaitForTerminalExitResponse{}, err
	}
	st := readStatus(t)
	if st == nil {
		return api.WaitForTerminalExitResponse{}, nil
	}
	return api.WaitForTerminalExitResponse{ExitCode: st.ExitCode, Signal: st.Signal}, nil
}

// kill answers terminal/kill: tears the tmux window down (best-effort — a
// command that already exited on its own leaves no window to kill, which is
// not an error here) and unconditionally releases the terminal's wait-for
// channel afterward.
//
// That release matters even for an already-finished command: kill-window
// destroys the window OUTRIGHT (unlike a natural exit, which merely leaves
// a dead pane behind — remain-on-exit's whole point), which means the
// wrapper script never reaches its own `tmux wait-for -S` line if it was
// still running. Without this, any WaitForTerminalExit call parked on this
// terminal's channel would block forever.
//
// A narrow, accepted race: if the command finishes naturally in the window
// between kill-window's own race check and this function's status read, the
// terminal is reported as killed (Signal SIGHUP — what tmux kill-window
// actually delivers to the pane's foreground process group by closing its
// pty) even though it had just exited with a real code. This matches the
// same race every terminal-owning editor's kill implementation accepts.
func (l *localTerminals) kill(ctx context.Context, req api.KillTerminalRequest) (api.KillTerminalResponse, error) {
	t, ok := l.lookup(req.TerminalId)
	if !ok {
		return api.KillTerminalResponse{}, fmt.Errorf("unknown terminal %q", req.TerminalId)
	}
	_, _ = l.runner.Run(ctx, "kill-window", "-t", t.window)

	t.mu.Lock()
	t.killed = true
	if t.exitStatus == nil {
		if b, err := os.ReadFile(t.statusPath); err == nil {
			if code, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
				t.exitStatus = &api.TerminalExitStatus{ExitCode: &code}
			}
		}
		if t.exitStatus == nil {
			sig := "SIGHUP"
			t.exitStatus = &api.TerminalExitStatus{Signal: &sig}
		}
	}
	t.mu.Unlock()

	_, _ = l.runner.Run(ctx, "wait-for", "-S", t.channel)
	return api.KillTerminalResponse{}, nil
}

// release answers terminal/release: forgets the handle and frees its
// resources — ReleaseTerminalRequest's own doc: "free its resources".
// kill-window runs UNCONDITIONALLY, not only for a still-running terminal: a
// naturally-exited command leaves remain-on-exit's dead pane (and its
// window) sitting in the tmux session forever otherwise — measured, driving
// this end to end, a released-but-never-killed window survived in
// `tmux -L ctxloom-acp-terminal list-windows` after the whole chat ended.
// kill-window on an already-dead or already-gone window is harmless (an
// ignored error), so there is no cost to always trying. Releasing an
// already-released or unknown id is a benign no-op, matching
// ReleaseTerminalResponse's empty, error-free shape.
func (l *localTerminals) release(ctx context.Context, req api.ReleaseTerminalRequest) (api.ReleaseTerminalResponse, error) {
	l.mu.Lock()
	t, ok := l.terms[req.TerminalId]
	if ok {
		delete(l.terms, req.TerminalId)
	}
	l.mu.Unlock()
	if !ok {
		return api.ReleaseTerminalResponse{}, nil
	}

	_, _ = l.runner.Run(ctx, "kill-window", "-t", t.window)
	_, _ = l.runner.Run(ctx, "wait-for", "-S", t.channel)
	_ = os.Remove(t.outputPath)
	_ = os.Remove(t.statusPath)
	return api.ReleaseTerminalResponse{}, nil
}
