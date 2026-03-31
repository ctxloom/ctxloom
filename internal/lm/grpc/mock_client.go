package grpc

import (
	"context"
	"io"
)

// MockClient is a test double for the Client interface.
// Configure the function fields to control behavior in tests.
type MockClient struct {
	// InfoFunc is called when Info is invoked.
	InfoFunc func(ctx context.Context) (*PluginInfo, error)

	// RunFunc is called when Run is invoked.
	RunFunc func(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error)

	// RunWithModelInfoFunc is called when RunWithModelInfo is invoked.
	RunWithModelInfoFunc func(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (*RunResult, error)

	// KillFunc is called when Kill is invoked.
	KillFunc func()

	// Call tracking
	InfoCalls             int
	RunCalls              int
	RunWithModelInfoCalls int
	KillCalls             int
}

// Ensure MockClient implements Client interface.
var _ Client = (*MockClient)(nil)

// Info returns metadata about the plugin.
func (m *MockClient) Info(ctx context.Context) (*PluginInfo, error) {
	m.InfoCalls++
	if m.InfoFunc != nil {
		return m.InfoFunc(ctx)
	}
	return &PluginInfo{Name: "mock", Version: "1.0.0"}, nil
}

// Run executes the plugin and streams output to the provided writers.
func (m *MockClient) Run(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (int32, error) {
	m.RunCalls++
	if m.RunFunc != nil {
		return m.RunFunc(ctx, req, stdout, stderr)
	}
	return 0, nil
}

// RunWithModelInfo executes the plugin and returns both exit code and model info.
func (m *MockClient) RunWithModelInfo(ctx context.Context, req *RunRequest, stdout, stderr io.Writer) (*RunResult, error) {
	m.RunWithModelInfoCalls++
	if m.RunWithModelInfoFunc != nil {
		return m.RunWithModelInfoFunc(ctx, req, stdout, stderr)
	}
	return &RunResult{ExitCode: 0}, nil
}

// Kill terminates the plugin process.
func (m *MockClient) Kill() {
	m.KillCalls++
	if m.KillFunc != nil {
		m.KillFunc()
	}
}

// NewMockClient creates a new MockClient with default no-op behavior.
func NewMockClient() *MockClient {
	return &MockClient{}
}

// MockClientFactory returns a ClientFactory that always returns the provided mock.
func MockClientFactory(mock *MockClient) ClientFactory {
	return func(backendName string, verbosity int) (Client, error) {
		return mock, nil
	}
}
