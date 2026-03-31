package grpc

import (
	"context"
	"io"
)

// Client is the interface for interacting with an AI plugin.
// This interface enables dependency injection and testing.
type Client interface {
	// Info returns metadata about the plugin.
	Info(ctx context.Context) (*PluginInfo, error)

	// Run executes the plugin and streams output to the provided writers.
	// Returns the exit code.
	Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error)

	// RunWithModelInfo executes the plugin and returns both exit code and model info.
	RunWithModelInfo(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (*RunResult, error)

	// Kill terminates the plugin process.
	Kill()
}

// ClientFactory creates plugin clients.
// This type enables dependency injection for client creation.
type ClientFactory func(backendName string, verbosity int) (Client, error)

// Ensure PluginClient implements Client interface.
var _ Client = (*PluginClient)(nil)

// DefaultClientFactory returns the default factory that creates real plugin clients.
func DefaultClientFactory() ClientFactory {
	return func(backendName string, verbosity int) (Client, error) {
		return NewSelfInvokingClient(backendName, verbosity)
	}
}
