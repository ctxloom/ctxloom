package grpc

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

	// WatchSession opens a structured turn stream over a session's transcript:
	// new entries, response boundaries, and idle heartbeats arrive on the events
	// channel until it closes (stream end / ctx cancel); a fatal receive error
	// arrives on the errors channel. The plugin must stay alive for the stream.
	WatchSession(ctx context.Context, sessionID string) (<-chan *WatchEvent, <-chan error, error)

	// Chat drives the backend's StructuredChat capability over a bidirectional
	// stream: write messages (user turns, permission answers, turn cancels) to
	// the returned channel and close it to end input; read normalized turn
	// events from the events channel; a fatal error arrives on the errors
	// channel. Errors if the backend lacks the capability.
	Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error)

	// ListSessions returns the plugin's transcript-store metadata for its own
	// workspace.
	ListSessions(ctx context.Context) ([]agent.SessionMeta, error)

	// GetPlans returns a session's plan documents, keyed by harp.
	GetPlans(ctx context.Context, harp string) ([]agent.PlanFile, error)

	// Kill terminates the plugin process.
	Kill()
}

// ClientFactory creates plugin clients.
// This type enables dependency injection for client creation. The label is
// the resolved config label, carried into the self-invoked serve subprocess:
// with an empty label, serve falls back to a map-ordered scan over same-type
// config entries and may configure an arbitrary label's binary/args/env, so
// callers that resolved a specific label (run, oneshot, delegated agent_run) must pass it.
type ClientFactory func(backendName, label string, verbosity int) (Client, error)

// Ensure LLMRunner implements Client interface.
var _ Client = (*LLMRunner)(nil)

// DefaultClientFactory returns the default factory that creates real plugin clients.
func DefaultClientFactory() ClientFactory {
	return func(backendName, label string, verbosity int) (Client, error) {
		// Return an explicit nil interface on failure. Forwarding the concrete
		// (*LLMRunner, error) pair directly would box a typed nil into a
		// non-nil Client, and every `if client != nil` check downstream tests
		// the interface, not the pointer.
		runner, err := NewSelfInvokingClientForLabel(backendName, label, verbosity)
		if err != nil {
			return nil, err
		}
		return runner, nil
	}
}
