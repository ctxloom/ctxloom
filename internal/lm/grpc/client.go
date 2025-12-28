package grpc

import (
	"context"
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

// Run executes the plugin and streams output to the provided writers.
func (c *GRPCClient) Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error) {
	stream, err := c.client.Run(ctx, req)
	if err != nil {
		return 1, err
	}

	var exitCode int32
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 1, err
		}

		switch output := resp.Output.(type) {
		case *RunResponse_Stdout:
			_, _ = stdout.Write(output.Stdout)
		case *RunResponse_Stderr:
			_, _ = stderr.Write(output.Stderr)
		case *RunResponse_ExitCode:
			exitCode = output.ExitCode
		}
	}

	return exitCode, nil
}

// PluginClient manages the lifecycle of a plugin process.
type PluginClient struct {
	client *plugin.Client
	grpc   *GRPCClient
}

// NewPluginClient creates a new plugin client that spawns the given command.
// The command should be the path to the plugin binary (e.g., "scm" with args ["plugin", "serve", "claudecode"]).
func NewPluginClient(cmd string, args []string) (*PluginClient, error) {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "plugin",
		Output: io.Discard,
		Level:  hclog.Error,
	})

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins:         PluginMap,
		Cmd:             exec.Command(cmd, args...),
		AllowedProtocols: []plugin.Protocol{
			plugin.ProtocolGRPC,
		},
		Logger: logger,
	})

	// Connect via gRPC
	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, err
	}

	// Dispense the plugin
	raw, err := rpcClient.Dispense(PluginName)
	if err != nil {
		client.Kill()
		return nil, err
	}

	grpcClient, ok := raw.(*GRPCClient)
	if !ok {
		client.Kill()
		return nil, err
	}

	return &PluginClient{
		client: client,
		grpc:   grpcClient,
	}, nil
}

// NewSelfInvokingClient creates a plugin client that invokes "scm plugin serve <backend>".
// This is used when no external plugin binary is found.
func NewSelfInvokingClient(backendName string) (*PluginClient, error) {
	// Get the path to the current executable
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}

	return NewPluginClient(executable, []string{"plugin", "serve", backendName})
}

// Info returns metadata about the plugin.
func (p *PluginClient) Info(ctx context.Context) (*PluginInfo, error) {
	return p.grpc.Info(ctx)
}

// Run executes the plugin.
func (p *PluginClient) Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error) {
	return p.grpc.Run(ctx, req, stdout, stderr)
}

// Kill terminates the plugin process.
func (p *PluginClient) Kill() {
	if p.client != nil {
		p.client.Kill()
	}
}
