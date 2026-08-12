package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// These three tests are the behaviour proof for
// fix/host-controlled-mcp-discovery: they assert on what actually happens
// (a real socket connection, a real error message, a real "no marker"
// verdict) — never on a function merely returning nil.
//
// None of these tests run in parallel with the rest of the package: they
// chdir the whole test process to simulate the shim's real precondition
// (it always runs IN the cell's own cwd), which would race any concurrently
// running test that also depends on process cwd.

// shortRuntimeDir returns a fresh directory suitable for XDG_RUNTIME_DIR in
// these tests. NOT t.TempDir(): that embeds the FULL test name (which these
// discovery tests deliberately make long and descriptive) into the path, and
// runnerSocketPath's candidate() rejects any socket dir whose resulting
// mcp-<pid>.sock path exceeds the unix sun_path headroom (100 chars,
// mcp_runner.go). Under a short-but-nonempty TMPDIR this test name alone can
// push the path over that limit, silently landing runnerSocketPath on the
// private-temp tier instead of socketKindHostRuntime — no marker gets
// published at all, and the test fails for a reason that has nothing to do
// with discovery correctness. A short, test-name-independent prefix keeps
// these tests' pass/fail tied to the behaviour under test.
func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// deadProcessPID returns a pid that is guaranteed NOT to name a live
// process: a child this test starts, waits for, and fully reaps.
func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())
	return pid
}

// dialForwardClient connects to socket exactly as runMCPForward does, and
// returns the connected session so the caller can assert on what the OTHER
// end actually says — the payload, not just that Connect returned nil.
func dialForwardClient(t *testing.T, socket string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestMCPDiscovery_ShimReachesRealRunnerWithoutEnvVar simulates the exact
// codex-acp scenario: CTXLOOM_MCP_SOCKET is absent (the adapter dropped
// mcpServers.env) but a real runner is up. The shim's discovery (not a
// mock) must find it and the connection must be to THAT runner — proven by
// reading back its own session instructions over the wire, not merely by a
// non-error return.
func TestMCPDiscovery_ShimReachesRealRunnerWithoutEnvVar(t *testing.T) {
	testsupport.Isolate(t) // clears CTXLOOM_MCP_SOCKET (t.Setenv-based, auto-restores)
	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	cellDir := t.TempDir()
	testsupport.ChangeDir(t, cellDir)

	const harp = "codex-drop-scenario-harp"
	endpoint, err := ServeRunnerMCP(testConfig(), harp, testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	// This is the shim's own discovery call (mcp_server.go's second tier),
	// invoked exactly as ServeStdio invokes it, with no env var in
	// play at all.
	sock, derr := probeWellKnownRunner(cellDir)
	require.NoError(t, derr)
	require.NotEmpty(t, sock, "well-known discovery must find the runner with NO env var set")
	assert.Equal(t, endpoint.SocketPath, sock, "discovered socket must be the REAL runner's own socket")

	// Payload assertion: actually connect over the discovered socket and
	// read back THIS runner's own instructions (they embed its harp) —
	// proof this is the real coordinator, not a stand-in that happens to
	// return a path.
	cs := dialForwardClient(t, sock)
	init := cs.InitializeResult()
	require.NotNil(t, init)
	assert.Contains(t, init.Instructions, harp, "connected session must be the real runner's, identified by its own harp")
}

// TestMCPDiscovery_RunnerAnchorClosesHostWorktreeCwdGap is the behaviour
// proof for fix/host-discovery-anchor: the RUNNER (`ctxloom llm serve`) is
// spawned with no cmd.Dir and inherits the coordinator's cwd
// (internal/lm/grpc/client.go), while the harness it hosts is launched with
// cmd.Dir=the per-agent worktree (internal/lm/backends/launcher.go) — the
// SAME dir the shim's `ctxloom mcp` process later runs in. Before this fix,
// ServeRunnerMCP always keyed its marker off its own os.Getwd() (the
// coordinator's cwd), so a workspace:worktree run's marker and the shim's
// probe derived DIFFERENT keys and discovery silently missed.
//
// This simulates that exact gap: the test process's real cwd (what
// os.Getwd() would return without the fix) is one temp dir, but the runner
// is handed a DIFFERENT dir as cellWorkDir (standing in for the
// coordinator's CTXLOOM_CELL_WORKDIR injection carrying ws.Dir()). The shim
// probes the worktree dir, exactly as it would with cwd=RunOptions.WorkDir.
func TestMCPDiscovery_RunnerAnchorClosesHostWorktreeCwdGap(t *testing.T) {
	testsupport.Isolate(t) // clears CTXLOOM_MCP_SOCKET/CTXLOOM_CELL_WORKDIR (t.Setenv-based, auto-restores)
	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// runnerProcCwd stands in for the coordinator's cwd the runner process
	// actually inherits (its os.Getwd()). workDir stands in for the
	// per-agent worktree the child engine — and thus the shim — actually
	// runs in. They are deliberately DIFFERENT directories.
	runnerProcCwd := t.TempDir()
	workDir := t.TempDir()
	require.NotEqual(t, runnerProcCwd, workDir, "the gap this test proves requires distinct dirs")
	testsupport.ChangeDir(t, runnerProcCwd)

	const harp = "worktree-anchor-harp"

	t.Run("without the anchor, mismatched cwds miss (the pre-fix bug)", func(t *testing.T) {
		endpoint, err := ServeRunnerMCP(testConfig(), harp, testHome(t), false, "")
		require.NoError(t, err)
		defer endpoint.Close()

		sock, derr := probeWellKnownRunner(workDir)
		require.NoError(t, derr)
		assert.Empty(t, sock, "keyed off the runner's own cwd, the marker must NOT be found from the worktree's cwd")
	})

	t.Run("with the anchor, runner and shim agree across the cwd gap", func(t *testing.T) {
		endpoint, err := ServeRunnerMCP(testConfig(), harp, testHome(t), false, workDir)
		require.NoError(t, err)
		defer endpoint.Close()

		// The marker must have been written under workDir's key, not
		// runnerProcCwd's — assert the derivation directly.
		name, ok := discoveryMarkerName(socketKindHostRuntime, workDir)
		require.True(t, ok)
		markerPath := filepath.Join(runtimeDir, "ctxloom", name)
		_, statErr := os.Stat(markerPath)
		assert.NoError(t, statErr, "marker must be published under the worktree's key, not the runner process's own cwd")

		// The shim always probes from ITS OWN cwd (=the child's WorkDir);
		// simulate that exactly, without ever touching the runner process's
		// real cwd.
		sock, derr := probeWellKnownRunner(workDir)
		require.NoError(t, derr)
		require.NotEmpty(t, sock, "the shim probing from the worktree's cwd must find the runner via the anchored key")
		assert.Equal(t, endpoint.SocketPath, sock, "discovered socket must be the real runner's own socket")

		cs := dialForwardClient(t, sock)
		init := cs.InitializeResult()
		require.NotNil(t, init)
		assert.Contains(t, init.Instructions, harp, "connected session must be the real runner's, identified by its own harp")
	})
}

// TestMCPDiscovery_FailsLoudWhenExpectedRunnerIsUnreachable covers the
// "should have a runner, can't find it" branch: a discovery marker exists
// and names a PID that is still alive, but its socket refuses the
// connection. This must never fall back to a silent local coordinator —
// exactly the failure mode (a rogue second coordinator whose agent_send
// can never reach the real parent) this whole fix exists to close.
func TestMCPDiscovery_FailsLoudWhenExpectedRunnerIsUnreachable(t *testing.T) {
	testsupport.Isolate(t) // clears CTXLOOM_MCP_SOCKET (t.Setenv-based, auto-restores)
	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	cellDir := t.TempDir()
	testsupport.ChangeDir(t, cellDir)

	markerDir := filepath.Join(runtimeDir, "ctxloom")
	require.NoError(t, os.MkdirAll(markerDir, 0o700))
	name, ok := discoveryMarkerName(socketKindHostRuntime, cellDir)
	require.True(t, ok)
	deadSocket := filepath.Join(t.TempDir(), "nobody-home.sock") // never bound by anyone
	marker := runnerDiscoveryMarker{Socket: deadSocket, Pid: os.Getpid()}
	raw, err := json.Marshal(marker)
	require.NoError(t, err)
	markerPath := filepath.Join(markerDir, name)
	require.NoError(t, os.WriteFile(markerPath, raw, 0o600))

	// Drive the ACTUAL shim entry point — ServeStdio is the whole body of
	// `ctxloom mcp serve` (internal/cli's cobra RunE adds only the signal
	// context, the cwd, and the fail-loud gate), and it returns this error
	// before ever touching stdio (no risk of the test hanging on stdin), so
	// this is the real wiring, not a stand-in for it.
	runErr := ServeStdio(context.Background(), cellDir, nil)
	require.Error(t, runErr, "the shim must fail loud, not silently fall back to a local coordinator")
	assert.Contains(t, runErr.Error(), "refusing to silently start a local coordinator")
	assert.Contains(t, runErr.Error(), deadSocket, "the error must name the unreachable socket")
	assert.Contains(t, runErr.Error(), fmt.Sprintf("pid %d", os.Getpid()), "the error must name the live pid that makes this NOT a stale marker")

	// The marker names a runner that IS alive by construction: a self-heal
	// here would be wrong, so it must survive untouched.
	_, statErr := os.Stat(markerPath)
	assert.NoError(t, statErr, "a live runner's marker must not be removed")
}

// TestMCPDiscovery_StandaloneSessionGetsLocalMode is the no-regression
// check: a session with no env var and no discovery marker anywhere for
// its cwd (nothing ever claimed this workspace as runner-backed) must
// still get local mode — the pre-existing, legitimate `ctxloom mcp serve`
// behaviour for a bare stdio session.
func TestMCPDiscovery_StandaloneSessionGetsLocalMode(t *testing.T) {
	testsupport.Isolate(t) // clears CTXLOOM_MCP_SOCKET (t.Setenv-based, auto-restores)
	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	cellDir := t.TempDir()

	sock, derr := probeWellKnownRunner(cellDir)
	assert.NoError(t, derr, "a genuinely standalone session must not fail loud")
	assert.Empty(t, sock, "no marker anywhere means local mode is correct, not forward mode")

	t.Run("stale marker from a dead runner self-heals to local, not fail-loud", func(t *testing.T) {
		markerDir := filepath.Join(runtimeDir, "ctxloom")
		require.NoError(t, os.MkdirAll(markerDir, 0o700))
		name, ok := discoveryMarkerName(socketKindHostRuntime, cellDir)
		require.True(t, ok)
		markerPath := filepath.Join(markerDir, name)
		deadSocket := filepath.Join(t.TempDir(), "long-gone.sock")
		marker := runnerDiscoveryMarker{Socket: deadSocket, Pid: deadProcessPID(t)}
		raw, err := json.Marshal(marker)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(markerPath, raw, 0o600))

		sock, derr := probeWellKnownRunner(cellDir)
		assert.NoError(t, derr, "a dead runner's leftover marker must not fail loud")
		assert.Empty(t, sock, "no LIVE runner claims this workspace: standalone/local is correct")

		_, statErr := os.Stat(markerPath)
		assert.True(t, os.IsNotExist(statErr), "the stale marker should be cleaned up (self-heal)")
	})
}

// TestDiscoveryMarkerName_ContainerTierIsDeliberatelyCwdIndependent refutes
// a claim that the container tier's fixed "current.json" is a bug ("a marker
// written by one cell can be found by a shim in a different workspace") that
// proposed keying it by cwd like the host tier.
// Keying it by cwd is what would BREAK it:
//
//   - The two sides do NOT share a cwd inside a container. ServeRunnerMCP keys
//     the marker off coord.EnvCellWorkDir, which the host does not stamp for a
//     container spawn, so the runner falls back to its OWN os.Getwd(); the shim
//     runs in the engine child's cwd. A cwd-derived key would make the two
//     names disagree, the probe would find nothing, and the shim would stand up
//     the rogue local coordinator this whole file exists to prevent — the
//     silent no-op, reintroduced.
//   - There is nothing to disambiguate. A container hosts one agent, hence one
//     workspace, so no second cell can publish here.
//
// The host tier is the one that genuinely needs the cwd key, because several
// runners share $XDG_RUNTIME_DIR/ctxloom on one host; that asymmetry is the
// design, and this test pins both halves of it.
func TestDiscoveryMarkerName_ContainerTierIsDeliberatelyCwdIndependent(t *testing.T) {
	runnerName, ok := discoveryMarkerName(socketKindContainer, "/runner/cwd")
	require.True(t, ok, "the container tier publishes a marker")
	shimName, ok := discoveryMarkerName(socketKindContainer, "/an/entirely/different/cell/cwd")
	require.True(t, ok)

	assert.Equal(t, runnerName, shimName,
		"a container marker must be findable by a shim whose cwd differs from the runner's")

	hostA, ok := discoveryMarkerName(socketKindHostRuntime, "/work/cell-a")
	require.True(t, ok)
	hostB, ok := discoveryMarkerName(socketKindHostRuntime, "/work/cell-b")
	require.True(t, ok)
	assert.NotEqual(t, hostA, hostB,
		"the SHARED host runtime dir is where per-cell keying is required")

	_, ok = discoveryMarkerName(socketKindPrivateTemp, "/work/cell-a")
	assert.False(t, ok, "the private-temp tier publishes nothing discoverable")
}
