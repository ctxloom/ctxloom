package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/exitstatus"
	"github.com/ctxloom/ctxloom/internal/shared/procsig"
)

// This file is the stream-json transport the structured driver
// (streamJSONDriver, instance.go) runs a turn over: user messages are
// written to the process's stdin as NDJSON, and the NDJSON event stream from
// stdout is normalized (see chat_stream.go). No pty, no keystroke injection.

// ErrChatMCPTransportUnsupported is returned when an MCP server entry names a
// transport claude's --mcp-config file cannot express. Remedy: only
// MCPTransportStdio, MCPTransportHTTP, and MCPTransportSSE are valid. Aliases
// agent.ErrChatMCPConfigTransportUnsupported (the shared marshal helper's own
// sentinel) under this package's existing name, so callers checking
// errors.Is(err, ErrChatMCPTransportUnsupported) are unaffected by the move.
var ErrChatMCPTransportUnsupported = agent.ErrChatMCPConfigTransportUnsupported

// chatTransport is the I/O seam for a stream-json conversation: a writable stdin,
// a readable stdout, and the process's two endings. Default = a spawned
// `claude` process; tests inject in-memory pipes so they never spawn anything.
//
// The turn's CONTEXT is the interrupt: when it ends, the transport asks the
// process to stop (procsig.Interrupt) and kills it only after its grace, so
// stdout reaches EOF either way and the driver reads the process's last words
// rather than cutting them off.
type chatTransport struct {
	stdin  io.WriteCloser
	stdout io.Reader
	// close ends the process NOW and reaps it — the error paths' teardown.
	close func() error
	// wait reaps a process that is ending on its own (stdout reached EOF, or
	// the interrupt asked it to) and reports how it exited. nil: nothing to
	// reap, a clean exit.
	wait func() error
	// ended records that the TURN ended the process — a Close, or a reap that
	// outran its grace and killed it — so its exit status is ctxloom's doing,
	// not the engine's.
	ended atomic.Bool
}

// Close tears the transport down (and unblocks a reader parked on stdout).
func (t *chatTransport) Close() error {
	t.ended.Store(true)
	if t.close != nil {
		return t.close()
	}
	return nil
}

// Wait reaps the ended process and reports its exit.
func (t *chatTransport) Wait() error {
	if t.wait != nil {
		return t.wait()
	}
	return nil
}

// errTurnProcessDied is a turn whose engine process ended WITHOUT a result
// frame and exited in failure: it died mid-turn, which ends the run.
var errTurnProcessDied = errors.New("claude: the turn's process died before it answered")

type chatTransportFunc func(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*chatTransport, error)

// readChatEvents reads newline-delimited JSON from stdout (no line-length cap —
// tool outputs can be large) and maps each line to ChatEvents on out until EOF
// or a read error. It is NOT bounded by the turn's context: an interrupted
// process still has things to say on its way out, and the transport guarantees
// the EOF (a kill after its grace). Each entry is stamped with a receipt time
// (see stampEntryTime) since stream-json carries no per-event timestamp.
func readChatEvents(stdout io.Reader, out chan<- agent.ChatEvent, now func() time.Time) {
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		for _, ev := range mapStreamJSONEvent(line) {
			out <- stampEntryTime(ev, now)
		}
		if err != nil {
			return // EOF or read error (e.g. transport closed)
		}
	}
}

// stampEntryTime records receipt time on an entry event that arrived without a
// timestamp. claude-code's stream-json carries no per-event time, so the live
// chat stream always lands here; a transcript-derived entry already has one and
// is left untouched. Clock-injected so the fallback is deterministic in tests.
func stampEntryTime(ev agent.ChatEvent, now func() time.Time) agent.ChatEvent {
	if ev.Entry != nil && ev.Entry.Timestamp.IsZero() {
		ev.Entry.Timestamp = now()
	}
	return ev
}

// sjUserOut is the NDJSON user message written to stdin, matching claude's
// stream-json input schema: {"type":"user","message":{"role":"user","content":...}}.
type sjUserOut struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

func writeUserMessage(w io.Writer, text string) error {
	var m sjUserOut
	m.Type = "user"
	m.Message.Role = "user"
	m.Message.Content = text
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// flagInputFormat, flagVerbose, flagResume: chat-only argv flags that have no
// existing constant in enginecli.go (the oneshot/interactive EngineCLI
// declarations never emit them — this structured path is a third surface).
// flagPrint, flagOutputFormat, flagModel, flagMCPConfig are reused from
// enginecli.go rather than redeclared here, and permission-mode flags are
// reused from permissionArgs (claudecode.go) rather than re-mapped.
const (
	flagInputFormat = "--input-format"
	flagVerbose     = "--verbose"
	flagResume      = "--resume"
)

// turnInterruptGrace is how long an interrupted turn's process gets to unwind
// before it is killed, and how long the driver keeps relaying its last words.
const turnInterruptGrace = 10 * time.Second

// spawnChatTransport launches the real `claude` process with piped stdio (NOT a
// pty) under the default grace. stderr passes through for diagnostics.
func spawnChatTransport(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*chatTransport, error) {
	return spawnChatTransportGrace(ctx, binary, args, env, workDir, turnInterruptGrace)
}

// spawnChatTransportGrace is spawnChatTransport with the grace named. ctx
// ending interrupts the process (procsig.Interrupt, to the process group
// procsig.SpawnAttr made it lead) and exec kills it once grace has passed
// (WaitDelay) — the kill fires whether or not anyone is waiting yet.
func spawnChatTransportGrace(ctx context.Context, binary string, args []string, env map[string]string, workDir string, grace time.Duration) (*chatTransport, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = procsig.SpawnAttr()
	cmd.Cancel = func() error { return procsig.Interrupt(cmd.Process) }
	cmd.WaitDelay = grace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	reap := sync.OnceValue(cmd.Wait)
	tr := &chatTransport{
		stdin:  stdin,
		stdout: stdout,
		close: func() error {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			return reap()
		},
	}
	tr.wait = func() error {
		_ = stdin.Close()
		killed, err := reapWithin(reap, grace, cmd.Process)
		if killed {
			tr.ended.Store(true)
		}
		return err
	}
	return tr, nil
}

// reapWithin reaps a process whose stdout has ended; one that has not exited
// within grace is killed (killed) — its turn is over either way, and a wait
// that could hang would hold the turn open forever.
func reapWithin(reap func() error, grace time.Duration, p *os.Process) (killed bool, err error) {
	done := make(chan error, 1)
	go func() { done <- reap() }()
	select {
	case err := <-done:
		return false, err
	case <-time.After(grace):
		_ = p.Kill()
		return true, <-done
	}
}

// engineExit is the status the turn's process exited with ON ITS OWN
// (exitstatus.Of — the one computation every launch path shares), nil when
// there is none to report: the turn ended it (ctx's interrupt, a teardown, a
// reap past its grace), so a 130 or 137 would be ctxloom's signal read as the
// engine's failure; or the reap failed without an exit status.
func engineExit(ctx context.Context, tr *chatTransport, waitErr error) *int {
	if ctx.Err() != nil || tr.ended.Load() {
		return nil
	}
	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return nil
		}
		code = exitstatus.Of(ee)
	}
	return &code
}
