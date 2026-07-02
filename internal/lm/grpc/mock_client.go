package grpc

import (
	"context"
	"io"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// MockClient is a test double for the Client interface.
// Configure the function fields to control behavior in tests.
type MockClient struct {
	// InfoFunc is called when Info is invoked.
	InfoFunc func(ctx context.Context) (*LLMInfo, error)

	// RunFunc is called when Run is invoked.
	RunFunc func(ctx context.Context, req *RunStart, stdout, stderr io.Writer) (int32, error)

	// RunWithModelInfoFunc is called when RunWithModelInfo is invoked.
	RunWithModelInfoFunc func(ctx context.Context, req *RunStart, stdout, stderr io.Writer) (*RunResult, error)

	// GetSessionFunc is called when GetSession is invoked.
	GetSessionFunc func(ctx context.Context, sessionID string) (*agent.Session, error)

	// WatchSessionFunc is called when WatchSession is invoked.
	WatchSessionFunc func(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error)

	// ChatFunc is called when Chat is invoked.
	ChatFunc func(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error)

	// ListSessionsFunc is called when ListSessions is invoked.
	ListSessionsFunc func(ctx context.Context) ([]agent.SessionMeta, error)

	// GetPlansFunc is called when GetPlans is invoked.
	GetPlansFunc func(ctx context.Context, harp string) ([]agent.PlanFile, error)

	// KillFunc is called when Kill is invoked.
	KillFunc func()

	// Call tracking. Guarded by mu because the compactor distills chunks
	// concurrently through a single shared mock (MockClientFactory hands the
	// same instance to every clientFactory call), so Run/Kill race otherwise.
	mu                    sync.Mutex
	InfoCalls             int
	RunCalls              int
	RunWithModelInfoCalls int
	GetSessionCalls       int
	WatchSessionCalls     int
	ListSessionsCalls     int
	KillCalls             int
}

// Ensure MockClient implements Client interface.
var _ Client = (*MockClient)(nil)

// Info returns metadata about the plugin.
func (m *MockClient) Info(ctx context.Context) (*LLMInfo, error) {
	m.InfoCalls++
	if m.InfoFunc != nil {
		return m.InfoFunc(ctx)
	}
	return &LLMInfo{Name: "mock", Version: "1.0.0"}, nil
}

// Run executes the plugin and streams output to the provided writers. stdin and
// resize are accepted to satisfy the Client interface but ignored by the mock.
func (m *MockClient) Run(ctx context.Context, req *RunStart, _ io.Reader, stdout, stderr io.Writer, _ <-chan *WindowSize) (int32, error) {
	m.mu.Lock()
	m.RunCalls++
	m.mu.Unlock()
	if m.RunFunc != nil {
		return m.RunFunc(ctx, req, stdout, stderr)
	}
	return 0, nil
}

// RunWithModelInfo executes the plugin and returns both exit code and model info.
func (m *MockClient) RunWithModelInfo(ctx context.Context, req *RunStart, _ io.Reader, stdout, stderr io.Writer, _ <-chan *WindowSize) (*RunResult, error) {
	m.RunWithModelInfoCalls++
	if m.RunWithModelInfoFunc != nil {
		return m.RunWithModelInfoFunc(ctx, req, stdout, stderr)
	}
	return &RunResult{ExitCode: 0}, nil
}

// GetSession returns a normalized session, defaulting to an empty one.
func (m *MockClient) GetSession(ctx context.Context, sessionID string) (*agent.Session, error) {
	m.GetSessionCalls++
	if m.GetSessionFunc != nil {
		return m.GetSessionFunc(ctx, sessionID)
	}
	return &agent.Session{}, nil
}

// WatchSession returns a structured turn stream, defaulting to an immediately
// closed (empty) stream so callers ranging over it terminate at once.
func (m *MockClient) WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error) {
	m.mu.Lock()
	m.WatchSessionCalls++
	m.mu.Unlock()
	if m.WatchSessionFunc != nil {
		return m.WatchSessionFunc(ctx, sessionID)
	}
	events := make(chan *WatchEvent)
	errs := make(chan error)
	close(events)
	close(errs)
	return events, errs, nil
}

// Chat drives a structured chat; the default drains the input channel and
// returns immediately-closed event/error channels.
func (m *MockClient) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	if m.ChatFunc != nil {
		return m.ChatFunc(ctx, req)
	}
	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error)
	go func() { //nolint:revive // drain so the caller never blocks writing input
		for range in {
		}
	}()
	close(events)
	close(errs)
	return in, events, errs, nil
}

// ListSessions returns transcript metadata, defaulting to none.
func (m *MockClient) ListSessions(ctx context.Context) ([]agent.SessionMeta, error) {
	m.ListSessionsCalls++
	if m.ListSessionsFunc != nil {
		return m.ListSessionsFunc(ctx)
	}
	return nil, nil
}

// GetPlans returns a session's plan documents, defaulting to none.
func (m *MockClient) GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error) {
	if m.GetPlansFunc != nil {
		return m.GetPlansFunc(ctx, harp)
	}
	return nil, nil
}

// Kill terminates the plugin process.
func (m *MockClient) Kill() {
	m.mu.Lock()
	m.KillCalls++
	m.mu.Unlock()
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
	return func(backendName, label string, verbosity int) (Client, error) {
		return mock, nil
	}
}
