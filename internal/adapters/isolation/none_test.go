package isolation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// withStartHostRunner installs a stand-in for the bare self-invoked host
// runner spawn for the duration of the test, recording the argv and env it
// was handed.
func withStartHostRunner(t *testing.T, fn func(args []string, env map[string]string) (*pb.HostRunner, error)) {
	t.Helper()
	orig := startHostRunner
	startHostRunner = fn
	t.Cleanup(func() { startHostRunner = orig })
}

// TestNoneStartRunner_LaunchFailureNamesTheAgent pins that a failed host
// runner spawn used to surface pb.StartHostRunner's error verbatim, so a
// caller running a fan-out of members saw a bare exec failure with nothing
// saying WHICH agent's runner died. The wrapped error must name the backend
// and the member label while preserving the cause for errors.Is/As.
func TestNoneStartRunner_LaunchFailureNamesTheAgent(t *testing.T) {
	withStartHostRunner(t, func([]string, map[string]string) (*pb.HostRunner, error) {
		return nil, assert.AnError
	})

	_, err := None{}.StartRunner(context.Background(), "mock", "member-3", 0, hostWorkspace{dir: "/proj"}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError, "the cause must survive wrapping")
	assert.Contains(t, err.Error(), "mock", "the failure names the backend whose runner died")
	assert.Contains(t, err.Error(), "member-3", "the failure names the member label whose runner died")
}

// TestNoneStartRunner_CarriesTheCallersEnvAndNoCell: the runner learns its
// cell from the Launch, so the runner subprocess is started with the caller's
// per-spawn env (the reach-back trio) and no workspace stamp.
func TestNoneStartRunner_CarriesTheCallersEnvAndNoCell(t *testing.T) {
	var got map[string]string
	withStartHostRunner(t, func(_ []string, env map[string]string) (*pb.HostRunner, error) {
		got = env
		return nil, assert.AnError
	})

	caller := map[string]string{"CTXLOOM_COORD_URL": "http://host:9000"}
	_, _ = None{}.StartRunner(context.Background(), "mock", "m", 0, hostWorkspace{dir: "/ws"}, caller)

	assert.Equal(t, map[string]string{"CTXLOOM_COORD_URL": "http://host:9000"}, got, "the runner's env is the caller's per-spawn env and nothing more: its cell rides the Launch")
}
