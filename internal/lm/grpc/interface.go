package grpc

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/agent"
)

// Client is the interface for interacting with an AI plugin.
// This interface enables dependency injection and testing.
type Client interface {
	// Info returns metadata about the plugin.
	Info(ctx context.Context) (*LLMInfo, error)

	// Run executes the plugin and streams output to the provided writers,
	// pumping the frontend's stdin/resize (nil for non-interactive). Returns the
	// exit code.
	Run(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (int32, error)

	// RunWithModelInfo is Run plus the resolved model info.
	RunWithModelInfo(ctx context.Context, req *RunStart, stdin io.Reader, stdout, stderr io.Writer, resize <-chan *WindowSize) (*RunResult, error)

	// GetSession asks the plugin to materialize a transcript (by agent-agnostic
	// session id) into the normalized session form. No workspace is passed — the
	// agent server is self-situated.
	GetSession(ctx context.Context, sessionID string) (*agent.Session, error)

	// ListSessions returns the plugin's transcript-store metadata for its own
	// workspace.
	ListSessions(ctx context.Context) ([]agent.SessionMeta, error)

	// GetPlans returns a session's plan documents, keyed by harp.
	GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error)

	// Kill terminates the plugin process.
	Kill()
}

// ClientFactory creates plugin clients.
// This type enables dependency injection for client creation.
type ClientFactory func(backendName string, verbosity int) (Client, error)

// Ensure LLMRunner implements Client interface.
var _ Client = (*LLMRunner)(nil)

// DefaultClientFactory returns the default factory that creates real plugin clients.
func DefaultClientFactory() ClientFactory {
	return func(backendName string, verbosity int) (Client, error) {
		return NewSelfInvokingClient(backendName, verbosity)
	}
}
