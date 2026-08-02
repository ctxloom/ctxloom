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

	_, err := None{}.StartRunner(context.Background(), "kiro", "member-3", 0, hostWorkspace{dir: "/proj"}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError, "the cause must survive wrapping")
	assert.Contains(t, err.Error(), "kiro", "the failure names the backend whose runner died")
	assert.Contains(t, err.Error(), "member-3", "the failure names the member label whose runner died")
}

// TestNoneSpawnEnv_BothHalvesStampTheCellWorkDir pins the SHARED
// behaviour: SpawnClient and StartRunner reached the host spawn through two
// byte-identical copies of the same per-spawn env assembly, so a fix to one
// (this env is the runner's MCP discovery marker — see EnvCellWorkDir) could
// silently miss the other. Both halves must copy the caller's map rather than
// mutate it, and both must stamp the workspace dir under the SAME key.
func TestNoneSpawnEnv_BothHalvesStampTheCellWorkDir(t *testing.T) {
	var got map[string]string
	withStartHostRunner(t, func(_ []string, env map[string]string) (*pb.HostRunner, error) {
		got = env
		return nil, assert.AnError
	})

	caller := map[string]string{"CTXLOOM_COORD_URL": "http://host:9000"}
	_, _ = None{}.StartRunner(context.Background(), "kiro", "m", 0, hostWorkspace{dir: "/ws"}, caller)

	assert.Equal(t, "/ws", got[envCellWorkDir], "the workspace dir is stamped for the runner's discovery marker")
	assert.Equal(t, "http://host:9000", got["CTXLOOM_COORD_URL"], "the caller's per-spawn env rides along")
	assert.NotContains(t, caller, envCellWorkDir, "the caller's map is copied, never mutated")
}

// TestSpawnEnvWithCellWorkDir covers every arm of the shared assembly the two
// halves now collapse onto — a nil caller map, a nil workspace, a workspace
// with no directory, and the stamped case.
func TestSpawnEnvWithCellWorkDir(t *testing.T) {
	assert.Empty(t, spawnEnvWithCellWorkDir(nil, nil), "no caller env and no workspace stamps nothing")

	assert.Empty(t, spawnEnvWithCellWorkDir(nil, hostWorkspace{dir: ""}),
		"a workspace with no directory stamps nothing (an empty marker key would not discover anything)")

	assert.Equal(t, map[string]string{envCellWorkDir: "/ws"}, spawnEnvWithCellWorkDir(nil, hostWorkspace{dir: "/ws"}))

	caller := map[string]string{"A": "1"}
	assert.Equal(t, map[string]string{"A": "1", envCellWorkDir: "/ws"},
		spawnEnvWithCellWorkDir(caller, hostWorkspace{dir: "/ws"}))
	assert.Equal(t, map[string]string{"A": "1"}, caller, "the caller's map is never mutated")
}
