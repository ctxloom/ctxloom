package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// This file implements the StructuredChat capability for claude-code over its
// `--input-format stream-json` mode: user messages are written to the process's
// stdin as NDJSON, and the NDJSON event stream from stdout is normalized (see
// chat_stream.go) onto the outbound channel. No pty, no keystroke injection.

// ErrChatModelQuirkUnsupported is returned when ChatRequest.ModelQuirk is set.
// ModelDeliveryQuirk exists to route around a documented ACP-adapter model-
// delivery defect (claude-code-acp 0.16.2 ignoring every spec-standard model
// channel) by placing a non-spec JSON-RPC call right after an ACP handshake.
// This transport speaks claude's own stream-json wire directly — there is no
// ACP session, and therefore no handshake to place such a call after — so the
// quirk cannot be honored even in principle. Remedy: leave ChatRequest.ModelQuirk
// nil for claude-code; --model on the launch argv is this transport's only (and
// spec-standard) model channel.
var ErrChatModelQuirkUnsupported = errors.New("claude chat: ModelQuirk is not supported over stream-json (no ACP session exists to place the non-spec call after)")

// ErrChatForwardTerminalUnsupported is returned when ChatRequest.ForwardTerminal
// is true. claude's `-p --output-format stream-json` protocol has no terminal/*
// callback to broker to an upstream editor — accepting the flag and silently
// never emitting a ChatEvent.Terminal would be exactly the "succeeds without
// doing the thing" failure this project refuses to ship. Remedy: leave
// ChatRequest.ForwardTerminal false for claude-code (already the case for every
// delegated-child caller, which has no upstream editor to broker to).
var ErrChatForwardTerminalUnsupported = errors.New("claude chat: ForwardTerminal is not supported (stream-json has no terminal/* callback to broker)")

// ErrChatMCPTransportUnsupported is returned when a ChatRequest.MCPServers entry
// names a transport claude's --mcp-config file cannot express. Remedy: only
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

type chatTransportFunc func(ctx context.Context, args []string, env map[string]string, workDir string) (*chatTransport, error)

// Chat runs a structured conversation. It honors the agent.StructuredChat
// contract: the caller closes `in` to end input; this closes `out` exactly once
// before returning; it returns when input is closed and the final response
// drains, when ctx is cancelled, or on a fatal error.
func (b *ClaudeCode) Chat(parentCtx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)

	if req.ModelQuirk != nil {
		return ErrChatModelQuirkUnsupported
	}
	if req.ForwardTerminal {
		return ErrChatForwardTerminalUnsupported
	}

	// Runtime is deliberately NOT consulted here: by the time Chat runs, the
	// runner PROCESS this call executes inside has already been placed on the
	// requested runtime axis by the starter seam (docker-direct `ctxloom llm
	// host` for a container axis, a bare self-invoked `llm host` for host —
	// see internal/operations/delegate.go's PreparedAgentChat.StartEngine).
	// Chat only ever spawns `claude` as an ordinary child of THIS process, so
	// it inherits that isolation for free and needs no container logic of its
	// own.

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	mcpConfigPath, cleanupMCP, err := writeChatMCPConfig(req.MCPServers)
	if err != nil {
		return err
	}
	defer cleanupMCP()

	open := b.openChatTransport
	if open == nil {
		open = b.spawnChatTransport
	}
	tr, err := open(ctx, b.chatArgs(req, mcpConfigPath), req.Env, req.WorkDir)
	if err != nil {
		return err
	}

	// Reader: stdout NDJSON → normalized events → out. Closes readerDone on exit.
	readerDone := make(chan struct{})
	go readChatEvents(ctx, tr.stdout, out, readerDone, b.clock())

	// teardown closes the transport (unblocking a parked reader) then waits for
	// the reader to finish, so no send races the deferred close(out).
	teardown := func() {
		_ = tr.Close()
		<-readerDone
	}

	for {
		select {
		case <-ctx.Done():
			teardown()
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				// No more input: half-close stdin so claude completes the last
				// turn and exits, draining stdout to EOF; then tear down.
				_ = tr.stdin.Close()
				<-readerDone
				_ = tr.Close()
				return nil
			}
			if err := writeUserMessage(tr.stdin, msg.Text); err != nil {
				teardown()
				return err
			}
		}
	}
}

// clock returns the timestamp source for chat entries, defaulting to time.Now
// when none was injected.
func (b *ClaudeCode) clock() func() time.Time {
	if b.now != nil {
		return b.now
	}
	return time.Now
}

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

// chatArgs builds the stream-json command line: base args + the print/streaming
// flags, plus model, permissions, resume, and MCP config when requested.
// mcpConfigPath is "" when req.MCPServers is empty (no --mcp-config emitted).
func (b *ClaudeCode) chatArgs(req agent.ChatRequest, mcpConfigPath string) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)
	args = append(args, flagPrint,
		flagInputFormat, "stream-json",
		flagOutputFormat, "stream-json",
		flagVerbose,
	)
	if req.Model != "" {
		args = append(args, flagModel, req.Model)
	}
	// The attached servers' names, for the plan-posture grant. Inline rather
	// than a named helper: it had one caller and the extracted form was an
	// exact duplicate of four existing pluck-the-name loops.
	mcpNames := make([]string, 0, len(req.MCPServers))
	for _, s := range req.MCPServers {
		mcpNames = append(mcpNames, s.Name)
	}
	args = append(args, permissionArgs(req.Permissions, mcpNames)...)
	if req.ResumeSessionID != "" {
		args = append(args, flagResume, req.ResumeSessionID)
	}
	if mcpConfigPath != "" {
		args = append(args, flagMCPConfig, mcpConfigPath)
	}
	// Name the structured-chat session after ctxloom's harp, matching the
	// interactive path, so it's findable in the /resume picker.
	args = append(args, sessionNameArgs(req.Env)...)
	return args
}

// writeChatMCPConfig materializes req.MCPServers into a scratch claude
// .mcp.json for one Chat call and returns its path (for --mcp-config) plus a
// cleanup that removes the scratch directory. Returns ("", noop, nil) for an
// empty server set — no --mcp-config flag is then emitted, and buildArgs's own
// project-.mcp.json behavior (layered, not replaced) is unaffected.
//
// The document shape and write itself (mode 0o600, Env preserved verbatim —
// load-bearing for the coordinator's CTXLOOM_MCP_SOCKET stamp, see
// injectMCPSocketEnv in internal/core/coord/enginehost.go) are owned by
// agent.WriteChatMCPConfigFile; this function only manages the scratch
// directory a single Chat call needs the file to live in.
func writeChatMCPConfig(servers []agent.ChatMCPServer) (path string, cleanup func(), err error) {
	noop := func() {}
	if len(servers) == 0 {
		return "", noop, nil
	}

	dir, err := os.MkdirTemp("", "ctxloom-claude-chat-mcp-*")
	if err != nil {
		return "", noop, fmt.Errorf("claude chat: creating mcp config scratch dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, MCPFileName)
	if err := agent.WriteChatMCPConfigFile(path, servers); err != nil {
		cleanup()
		return "", noop, fmt.Errorf("claude chat: writing mcp config: %w", err)
	}
	return path, cleanup, nil
}

// spawnChatTransport launches the real `claude` process with piped stdio (NOT a
// pty). stderr passes through for diagnostics.
func (b *ClaudeCode) spawnChatTransport(ctx context.Context, args []string, env map[string]string, workDir string) (*chatTransport, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, args...)
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
