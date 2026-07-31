package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
)

// watchPoll exists only so tests can drive WatchSession's loop without waiting
// on real time; nothing in production writes it. That makes the DEFAULT arm the
// one production always takes and the one nothing else covers. A seam whose
// default silently became the test cadence would put every served WatchSession
// into a millisecond-scale re-read of the backend's whole transcript.
func TestWatchPollInterval_ProductionServersPollAtTheDefault(t *testing.T) {
	// The one production construction site, exercised as go-plugin drives it.
	p := &LLMGRPCPlugin{Impl: &fakeBackend{name: "claude-code"}}
	require.NoError(t, p.GRPCServer(nil, googlegrpc.NewServer()))

	// What that site builds: Impl set, every other field zero.
	srv := &GRPCServer{Impl: p.Impl}
	assert.Zero(t, srv.watchPoll, "no production path may set the test cadence")
	assert.Equal(t, defaultWatchPoll, srv.watchPollInterval(),
		"an unset seam must fall back to the production cadence")

	// And the seam still overrides when a test does set it.
	assert.Equal(t, defaultWatchPoll/2, (&GRPCServer{watchPoll: defaultWatchPoll / 2}).watchPollInterval())
}
