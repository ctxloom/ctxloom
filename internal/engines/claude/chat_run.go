package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
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
// a readable stdout, and a teardown. Default = a spawned `claude` process; tests
// inject in-memory pipes so they never spawn anything.
type chatTransport struct {
	stdin  io.WriteCloser
	stdout io.Reader
	close  func() error
}

// Close tears the transport down (and unblocks a reader parked on stdout).
func (t *chatTransport) Close() error {
	if t.close != nil {
		return t.close()
	}
	return nil
}

type chatTransportFunc func(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*chatTransport, error)

// readChatEvents reads newline-delimited JSON from stdout (no line-length cap —
// tool outputs can be large) and maps each line to ChatEvents on out, stopping
// on EOF/error or ctx cancellation. Each entry is stamped with a receipt time
// (see stampEntryTime) since stream-json carries no per-event timestamp.
func readChatEvents(ctx context.Context, stdout io.Reader, out chan<- agent.ChatEvent, done chan<- struct{}, now func() time.Time) {
	defer close(done)
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			for _, ev := range mapStreamJSONEvent(line) {
				select {
				case out <- stampEntryTime(ev, now):
				case <-ctx.Done():
					return
				}
			}
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

// spawnChatTransport launches the real `claude` process with piped stdio (NOT a
// pty). stderr passes through for diagnostics.
func spawnChatTransport(ctx context.Context, binary string, args []string, env map[string]string, workDir string) (*chatTransport, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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
	return &chatTransport{
		stdin:  stdin,
		stdout: stdout,
		close: func() error {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return cmd.Wait()
		},
	}, nil
}
