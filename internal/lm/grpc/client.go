package grpc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

// GRPCClient is the client-side implementation that communicates with the plugin.
type GRPCClient struct {
	client AIPluginClient
}

// Info returns metadata about the plugin.
func (c *GRPCClient) Info(ctx context.Context) (*PluginInfo, error) {
	return c.client.Info(ctx, &Empty{})
}

// RunResult contains the result of a Run call including model info.
type RunResult struct {
	ExitCode  int32
	ModelInfo *ModelInfo
}

// Run executes the plugin and streams output to the provided writers.
func (c *GRPCClient) Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error) {
	result, err := c.RunWithModelInfo(ctx, req, stdout, stderr)
	if err != nil {
		return 1, err
	}
	return result.ExitCode, nil
}

// RunWithModelInfo executes the plugin and returns both exit code and model info.
func (c *GRPCClient) RunWithModelInfo(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (*RunResult, error) {
	stream, err := c.client.Run(ctx, req)
	if err != nil {
		return nil, err
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

// PluginClient manages the lifecycle of a plugin process.
type PluginClient struct {
	conn pluginConnection
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

// pluginConnection is the abstraction over the hashicorp/go-plugin
// machinery that NewPluginClient depends on. Production
// dialPluginConnection wraps a real *plugin.Client; tests inject a
// fake that returns canned errors at each lifecycle step.
type pluginConnection interface {
	// Client returns the plugin's gRPC client interface, or an error if
	// the subprocess didn't come up.
	Client() (plugin.ClientProtocol, error)
	// Kill terminates the underlying subprocess. Idempotent.
	Kill()
}

// realPluginConnection adapts *plugin.Client to pluginConnection.
type realPluginConnection struct {
	client *plugin.Client
}

func (r *realPluginConnection) Client() (plugin.ClientProtocol, error) { return r.client.Client() }
func (r *realPluginConnection) Kill()                                  { r.client.Kill() }

// dialPluginConnection is the IoC seam tests override to avoid spawning
// real subprocesses. Production points it at the real go-plugin machinery.
var dialPluginConnection = func(cmd string, args []string, logger hclog.Logger) pluginConnection {
	return &realPluginConnection{client: plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap,
		Cmd:             exec.Command(cmd, args...),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	})}
}

// NewPluginClient creates a new plugin client that spawns the given command.
// The command should be the path to the plugin binary (e.g., "ctxloom" with args ["llm", "serve", "claudecode"]).
// Verbosity controls logging: 0=quiet, 1=info, 2=debug, 3+=trace.
func NewPluginClient(cmd string, args []string, verbosity int) (*PluginClient, error) {
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

	conn := dialPluginConnection(cmd, args, logger)

	// Connect via gRPC
	rpcClient, err := conn.Client()
	if err != nil {
		conn.Kill()
		return nil, err
	}

	// Dispense the plugin
	raw, err := rpcClient.Dispense(PluginName)
	if err != nil {
		conn.Kill()
		return nil, err
	}

	grpcClient, ok := raw.(*GRPCClient)
	if !ok {
		conn.Kill()
		return nil, fmt.Errorf("unexpected plugin type: %T", raw)
	}

	return &PluginClient{
		conn: conn,
		grpc: grpcClient,
	}, nil
}

// NewSelfInvokingClient creates a plugin client that invokes "ctxloom plugin serve <backend>".
// This is used when no external plugin binary is found.
// Verbosity controls logging: 0=quiet, 1=info, 2=debug, 3+=trace.
func NewSelfInvokingClient(backendName string, verbosity int) (*PluginClient, error) {
	// Get the path to the current executable
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}

	return NewPluginClient(executable, []string{"llm", "serve", backendName}, verbosity)
}

// Info returns metadata about the plugin.
func (p *PluginClient) Info(ctx context.Context) (*PluginInfo, error) {
	return p.grpc.Info(ctx)
}

// Run executes the plugin.
func (p *PluginClient) Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error) {
	return p.grpc.Run(ctx, req, stdout, stderr)
}

// RunWithModelInfo executes the plugin and returns both exit code and model info.
func (p *PluginClient) RunWithModelInfo(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (*RunResult, error) {
	return p.grpc.RunWithModelInfo(ctx, req, stdout, stderr)
}

// Kill terminates the plugin process.
func (p *PluginClient) Kill() {
	if p.conn != nil {
		p.conn.Kill()
	}
}
