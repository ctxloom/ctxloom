package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestNewDocMCPServer_ClosesItsHome pins U038-F15's leak half. The doc server
// is built on a coord.Home, and constructing one is not free: it opens a gRPC
// client and dispatches two background loops that go on redialling the
// deliberately-dead endpoint. Every call to NewDocMCPServer therefore leaked a
// connection and two goroutines for the life of the process — and the two list
// helpers build a fresh server on EVERY call, so the completeness gates that
// use them multiplied it.
//
// goleak compares the goroutine set around the call, so it fails on exactly the
// leak the row names rather than on a count that happens to drift.
func TestNewDocMCPServer_ClosesItsHome(t *testing.T) {
	defer goleak.VerifyNone(t,
		// gRPC's own package-level workers are process-wide, not per-Home.
		goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/transport.(*http2Client).keepalive"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*Subscriber).run"),
	)

	server, closeHome, err := NewDocMCPServer()
	require.NoError(t, err)
	require.NotNil(t, server)
	require.NotNil(t, closeHome, "the caller must be given a way to release the Home")
	closeHome()
}

// TestListDocMCPToolNames_ReleasesItsServer covers the helpers that build a
// server per call — the multiplier that turned one leaked Home into many.
func TestListDocMCPToolNames_ReleasesItsServer(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/transport.(*http2Client).keepalive"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*Subscriber).run"),
	)

	for range 3 {
		names, err := ListDocMCPToolNames(t.Context())
		require.NoError(t, err)
		assert.Contains(t, names, "agent_report", "the documented surface must actually be enumerated")
	}
}
