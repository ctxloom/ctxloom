package grpc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"

	"github.com/ctxloom/ctxloom/internal/selfexec"
)

// GRPCClient is the client-side implementation that communicates with the plugin.
type GRPCClient struct {
	client LLMClient
}

// Info returns metadata about the plugin.
func (c *GRPCClient) Info(ctx context.Context) (*LLMInfo, error) {
	return c.client.Info(ctx, &Empty{})
}

// RunResult contains the result of a Run call including model info.
type RunResult struct {
	ExitCode  int32
	ModelInfo *ModelInfo
}

// Run executes the plugin and streams output to the provided writers. stdin and
// resize are the frontend's terminal input (nil for non-interactive callers).
func (c *GRPCClient) Run(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (int32, error) {
	result, err := c.RunWithModelInfo(ctx, req, stdin, stdout, stderr, resize)
	if err != nil {
		return 1, err
	}
	return result.ExitCode, nil
}

// RunWithModelInfo executes the plugin over the bidirectional Run stream: it
// sends the RunStart, then pumps the frontend's stdin and resize events to the
// controller (which feeds them into the agent's pty), while consuming the
// response stream. The frontend owns the terminal; this is the client half of
// that ownership. gRPC forbids concurrent Send on one stream, so the start +
// stdin + resize pumps share a send mutex.
func (c *GRPCClient) RunWithModelInfo(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (*RunResult, error) {
	stream, err := c.client.Run(ctx)
	if err != nil {
		return nil, err
	}

	var sendMu sync.Mutex
	send := func(in *RunInput) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(in)
	}

	if err := send(&RunInput{Input: &RunInput_Start{Start: req}}); err != nil {
		return nil, fmt.Errorf("send run start: %w", err)
	}

	// Pump keystrokes. At end of run the goroutine is typically parked in
	// stdin.Read; it exits when that read returns (error, or a stray byte the
	// dead stream rejects). For the one-shot `ctxloom run` process the parked
	// read is moot — the process exits. A caller that keeps reading stdin in
	// the same process after Run returns (e.g. init's post-discovery relaunch
	// prompt) must NOT pass the raw os.Stdin here: the parked read would
	// swallow the next reader's input. Such callers pass a detachable lease
	// (see cmd's stdinHandoff) and detach it once Run returns, which unblocks
	// this goroutine and hands any in-flight bytes to the next reader.
	if stdin != nil {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, rerr := stdin.Read(buf)
				if n > 0 {
					if serr := send(&RunInput{Input: &RunInput_Stdin{Stdin: append([]byte(nil), buf[:n]...)}}); serr != nil {
						return
					}
				}
				if rerr != nil {
					return
				}
			}
		}()
	}

	// Pump terminal resizes.
	if resize != nil {
		go func() {
			for ws := range resize {
				if serr := send(&RunInput{Input: &RunInput_Resize{Resize: ws}}); serr != nil {
					return
				}
			}
		}()
	}

	result := &RunResult{}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch output := resp.Output.(type) {
		case *RunResponse_Stdout:
			_, _ = stdout.Write(output.Stdout)
		case *RunResponse_Stderr:
			_, _ = stderr.Write(output.Stderr)
		case *RunResponse_ExitCode:
			result.ExitCode = output.ExitCode
			// ModelInfo is sent with exit_code
			result.ModelInfo = resp.ModelInfo
		}
	}

	return result, nil
}

// LLMRunner manages the lifecycle of a plugin process.
type LLMRunner struct {
	conn llmConnection
	grpc *GRPCClient
}

// verbosityToHclogLevel converts verbosity count to hclog level.
// 0 = Error (discard most), 1 = Warn, 2 = Info, 3+ = Debug/Trace
func verbosityToHclogLevel(verbosity int) hclog.Level {
	switch {
	case verbosity >= 3:
		return hclog.Trace
	case verbosity == 2:
		return hclog.Debug
	case verbosity == 1:
		return hclog.Info
	default:
		return hclog.Error
	}
}

// llmConnection is the abstraction over the hashicorp/go-plugin
// machinery that NewLLMRunner depends on. Production
// dialLLMConnection wraps a real *plugin.Client; tests inject a
// fake that returns canned errors at each lifecycle step.
type llmConnection interface {
	// Client returns the plugin's gRPC client interface, or an error if
	// the subprocess didn't come up.
	Client() (plugin.ClientProtocol, error)
	// Kill terminates the underlying subprocess. Idempotent.
	Kill()
}

// realLLMConnection adapts *plugin.Client to llmConnection.
type realLLMConnection struct {
	client *plugin.Client
}

func (r *realLLMConnection) Client() (plugin.ClientProtocol, error) { return r.client.Client() }
func (r *realLLMConnection) Kill()                                  { r.client.Kill() }

// dialLLMConnection is the IoC seam tests override to avoid spawning
// real subprocesses. Production points it at the real go-plugin machinery.
var dialLLMConnection = func(cmd string, args []string, logger hclog.Logger) llmConnection {
	return &realLLMConnection{client: plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap,
		Cmd:             exec.Command(cmd, args...),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	})}
}

// NewLLMRunner creates a new plugin client that spawns the given command.
// The command should be the path to the plugin binary (e.g., "ctxloom" with args ["llm", "serve", "claudecode"]).
// Verbosity controls logging: 0=quiet, 1=info, 2=debug, 3+=trace.
func NewLLMRunner(cmd string, args []string, verbosity int) (*LLMRunner, error) {
	level := verbosityToHclogLevel(verbosity)
	output := io.Discard
	if verbosity > 0 {
		output = os.Stderr
	}

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin",
		Output: output,
		Level:  level,
	})

	conn := dialLLMConnection(cmd, args, logger)

	// Connect via gRPC
	rpcClient, err := conn.Client()
	if err != nil {
		conn.Kill()
		return nil, err
	}

	// Dispense the plugin
	raw, err := rpcClient.Dispense(LLMPluginKey)
	if err != nil {
		conn.Kill()
		return nil, err
	}

	grpcClient, ok := raw.(*GRPCClient)
	if !ok {
		conn.Kill()
		return nil, fmt.Errorf("unexpected plugin type: %T", raw)
	}

	return &LLMRunner{
		conn: conn,
		grpc: grpcClient,
	}, nil
}

// NewSelfInvokingClient creates a plugin client that invokes "ctxloom llm serve <backend>".
// This is used when no external plugin binary is found.
// Verbosity controls logging: 0=quiet, 1=info, 2=debug, 3+=trace.
func NewSelfInvokingClient(backendName string, verbosity int) (*LLMRunner, error) {
	return NewSelfInvokingClientForLabel(backendName, "", verbosity)
}

// NewSelfInvokingClientForLabel is NewSelfInvokingClient carrying the resolved
// config label into the serve subprocess. With two labels of the same backend
// type, serve's type-based lookup is map-ordered — the run would randomly
// apply either label's binary/args/env per process. Callers that resolved a
// specific label (the run path) pass it so serve configures exactly that
// entry; label may be empty when only the type is known.
func NewSelfInvokingClientForLabel(backendName, label string, verbosity int) (*LLMRunner, error) {
	// Resolve the running binary upgrade-safely: after an in-place upgrade,
	// bare os.Executable() reports "/path/ctxloom (deleted)" on Linux, which a
	// long-running MCP server (distill/recover tools) would then exec and
	// fail. selfexec strips the suffix and falls back to a PATH lookup.
	executable := selfexec.Path()

	args := []string{"llm", "serve", backendName}
	if label != "" {
		args = append(args, "--label", label)
	}
	return NewLLMRunner(executable, args, verbosity)
}

// Info returns metadata about the plugin.
func (p *LLMRunner) Info(ctx context.Context) (*LLMInfo, error) {
	return p.grpc.Info(ctx)
}

// Run executes the plugin.
func (p *LLMRunner) Run(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (int32, error) {
	return p.grpc.Run(ctx, req, stdin, stdout, stderr, resize)
}

// RunWithModelInfo executes the plugin and returns both exit code and model info.
func (p *LLMRunner) RunWithModelInfo(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (*RunResult, error) {
	return p.grpc.RunWithModelInfo(ctx, req, stdin, stdout, stderr, resize)
}

// Kill terminates the plugin process.
func (p *LLMRunner) Kill() {
	if p.conn != nil {
		p.conn.Kill()
	}
}
