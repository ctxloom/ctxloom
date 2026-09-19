package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestNewDocMCPServer_ClosesItsHome pins the leak. The doc server
// is built on a coord.Home, and constructing one is not free: it opens a gRPC
// client and dispatches two background loops that go on redialling the
// deliberately-dead endpoint. Every call to NewDocMCPServer therefore leaked a
// connection and two goroutines for the life of the process — and the two list
// helpers build a fresh server on EVERY call, so the completeness gates that
// use them multiplied it.
//
// goleak compares the goroutine set around the call, so it fails on exactly
// this leak rather than on a count that happens to drift.
func TestNewDocMCPServer_ClosesItsHome(t *testing.T) {
	// IgnoreCurrent snapshots the goroutines already running, so this measures
	// only what THIS call adds and leaves behind — the package's other tests
	// (real coordinators, engine fakes) run goroutines of their own that have
	// nothing to do with the Home under test.
	ignore := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, ignore,
		// gRPC's own package-level workers are process-wide, not per-Home, and
		// can be created lazily inside the window IgnoreCurrent opens.
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
	ignore := goleak.IgnoreCurrent()
	defer goleak.VerifyNone(t, ignore,
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
