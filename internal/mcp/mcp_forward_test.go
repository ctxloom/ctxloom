package mcp

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/version"
)

// captureStderr (this package's shared test helper — see testhelpers_test.go)
// redirects the real os.Stderr for the duration of fn and returns everything
// written to it: the diagnostics under test here (clidiag.Warn,
// fmt.Fprintf(os.Stderr, ...)) write to the real fd, not to any buffer a
// *cobra.Command might own.

// TestForward_UnixSocketRoundTrip drives the shim's forward transport
// end-to-end against a live runner MCP endpoint: serve on a unix socket,
// connect exactly as runMCPForward does, and mirror the toolset — the two
// modes of `ctxloom mcp` (forward-to-runner, bare local) both covered by
// tests (playbook deliverable 1).
func TestForward_UnixSocketRoundTrip(t *testing.T) {
	endpoint, err := ServeRunnerMCP(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.SocketPath)
			},
		}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	server, err := buildForwardServer(context.Background(), cs)
	require.NoError(t, err)

	// The mirrored stdio surface carries the whole classified toolset.
	tools := listServerTools(t, server)
	for name := range mcpschema.Routes() {
		_, ok := tools[name]
		assert.True(t, ok, "forwarded surface is missing %q", name)
	}
}

// TestBuildForwardServer_RefusesAZeroToolRunner pins that runMCPForward's
// own doc claims "an unreachable runner socket is a hard startup error: a
// silently-empty toolset would be a wrong-context session" — but nothing
// ever checked that ANY tool actually got registered once the connection
// itself succeeded. A runner that connects fine but advertises zero tools
// (a plausible wrong-context shape: e.g. a leaf session's surface computed
// against the wrong routing table) used to stand up a perfectly
// healthy-looking, entirely empty forward server.
func TestBuildForwardServer_RefusesAZeroToolRunner(t *testing.T) {
	empty := mcp.NewServer(&mcp.Implementation{Name: "empty-runner", Version: "test"}, nil)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := empty.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	_, err = buildForwardServer(context.Background(), cs)
	require.Error(t, err, "a runner advertising zero tools must refuse to stand up a forward session, not silently succeed empty")
	assert.Contains(t, err.Error(), "zero tools")
}

// TestDialReachBackSocket_UnixDefault pins the unchanged default: any plain
// path (no tcp:// marker) dials as a unix socket, exactly as before the
// off-Linux fallback existed.
func TestDialReachBackSocket_UnixDefault(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/mcp.sock"
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			_ = conn.Close()
		}
	}()

	conn, err := dialReachBackSocket(context.Background(), sock)
	require.NoError(t, err, "a plain path must still dial as unix")
	_ = conn.Close()
}

// TestDialReachBackSocket_TCPFallback pins the off-Linux fallback's far side:
// a "tcp://host:port" value dials TCP, not unix — the exact form
// internal/acp/container_transport.go's containerReachBackEnv emits on
// darwin/windows.
func TestDialReachBackSocket_TCPFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			_ = conn.Close()
		}
	}()

	conn, err := dialReachBackSocket(context.Background(), "tcp://"+ln.Addr().String())
	require.NoError(t, err, "a tcp:// value must dial TCP")
	_ = conn.Close()
}

// TestForward_TCPFallbackRoundTrip is TestForward_UnixSocketRoundTrip's
// off-Linux twin: the SAME runner MCP endpoint, reached over a TCP bridge
// (standing in for internal/acp/container_transport.go's reachBackBridge)
// instead of the unix socket directly, dialed via dialReachBackSocket exactly
// as runMCPForward would. Proves the far-side dial change is not just wiring
// — the whole forwarded toolset survives the TCP hop.
func TestForward_TCPFallbackRoundTrip(t *testing.T) {
	endpoint, err := ServeRunnerMCP(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	// A minimal TCP<->unix bridge, mirroring reachBackBridge without
	// importing internal/acp (which would cycle back into this package).
	bridgeLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bridgeLn.Close() })
	go func() {
		for {
			conn, aerr := bridgeLn.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				uc, uerr := net.Dial("unix", endpoint.SocketPath)
				if uerr != nil {
					return
				}
				defer func() { _ = uc.Close() }()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(uc, c); close(done) }()
				_, _ = io.Copy(c, uc)
				<-done
			}(conn)
		}
	}()

	tcpAddr := "tcp://" + bridgeLn.Addr().String()

	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: "test"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialReachBackSocket(ctx, tcpAddr)
			},
		}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	server, err := buildForwardServer(context.Background(), cs)
	require.NoError(t, err)

	tools := listServerTools(t, server)
	for name := range mcpschema.Routes() {
		_, ok := tools[name]
		assert.True(t, ok, "forwarded surface (over the TCP fallback) is missing %q", name)
	}
}

// TestPrepareForward_EmitsPreForwardDiagnostic is the behaviour proof for
// graceful-egomaniac unit 1 ("forwarding is never silent"): on an ACCEPTED
// forward, prepareForward must print a stderr diagnostic naming what
// triggered it, the socket, and the target runner's identity — BEFORE
// returning a session ready to proxy tool traffic. The measured incident
// this fixes was three silent hijacks in one day (once via the env var,
// twice via the discovery marker) with zero diagnostic either way.
func TestPrepareForward_EmitsPreForwardDiagnostic(t *testing.T) {
	testsupport.Isolate(t) // no CTXLOOM_SESSION_HARP set: nothing to mismatch, forward is accepted
	// Race guard: testHome's coord.Home runs a background reconnect-loop
	// goroutine that calls clidiag.WarnOnce, which falls back to reading the
	// bare os.Stderr var when no sink is installed — racing captureStderr's
	// os.Stderr reassignment below. Installing a sink first routes that
	// goroutine's warnings away from os.Stderr for the rest of the test.
	captureWarnings(t)
	endpoint, err := ServeRunnerMCP(testConfig(), "diagnostic-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	trigger := forwardTrigger{Kind: "env var", Name: "CTXLOOM_MCP_SOCKET"}
	var cs *mcp.ClientSession
	var outcome forwardOutcome
	captured := captureStderr(t, func() {
		cs, outcome, err = prepareForward(context.Background(), trigger, endpoint.SocketPath)
	})
	require.NoError(t, err)
	require.NotNil(t, cs)
	t.Cleanup(func() { _ = cs.Close() })
	assert.Equal(t, forwardOutcomeServed, outcome)

	assert.Contains(t, captured, "env var", "diagnostic must name WHICH trigger kind fired")
	assert.Contains(t, captured, "CTXLOOM_MCP_SOCKET", "diagnostic must name the trigger by name")
	assert.Contains(t, captured, endpoint.SocketPath, "diagnostic must name the socket path")
	assert.Contains(t, captured, "diagnostic-harp", "diagnostic must name the target runner's identity")
}

// TestPrepareForward_RefusesOnSessionIdentityMismatch is the behaviour proof
// for graceful-egomaniac unit 2's harp axis: when THIS session has its own
// CTXLOOM_SESSION_HARP and it disagrees with the connected runner's, the
// forward must be REFUSED (forwardOutcomeRefused, nil error, cs already
// closed) rather than silently adopting the foreign runner's identity — the
// measured hijack this whole fix exists to close.
func TestPrepareForward_RefusesOnSessionIdentityMismatch(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("CTXLOOM_SESSION_HARP", "this-session-harp")
	// warnings is BOTH the race guard (see TestPrepareForward_EmitsPreForwardDiagnostic)
	// AND where the refusal itself lands: prepareForward's refusal line goes
	// through clidiag.Warn, which this redirect routes to warnings rather
	// than os.Stderr — so it will NOT appear in captureStderr's capture below.
	warnings := captureWarnings(t)
	endpoint, err := ServeRunnerMCP(testConfig(), "someone-elses-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	trigger := forwardTrigger{Kind: "discovery marker", Name: "/fake/marker/path.json"}
	var cs *mcp.ClientSession
	var outcome forwardOutcome
	captureStderr(t, func() {
		cs, outcome, err = prepareForward(context.Background(), trigger, endpoint.SocketPath)
	})
	require.NoError(t, err, "a refusal is not itself an error")
	assert.Nil(t, cs, "a refused connection must already be closed, not handed back")
	assert.Equal(t, forwardOutcomeRefused, outcome)

	refusal := warnings.String()
	assert.Contains(t, refusal, "refusing", "the refusal must be loud, not silent")
	assert.Contains(t, refusal, "this-session-harp")
	assert.Contains(t, refusal, "someone-elses-harp")
}

// TestPrepareForward_RefusesOnBuildStampMismatch is unit 2's stamp axis: a
// runner built from a different revision than this process is refused even
// when identity matches (or is unset) — a mixed-build session (e.g. a
// runner left running across a `just build`) is exactly the kind of
// silently-wrong state this fix exists to surface rather than paper over.
func TestPrepareForward_RefusesOnBuildStampMismatch(t *testing.T) {
	testsupport.Isolate(t) // no CTXLOOM_SESSION_HARP: isolates this test to the stamp axis alone
	// warnings is BOTH the race guard (see TestPrepareForward_EmitsPreForwardDiagnostic)
	// AND where the refusal itself lands (clidiag.Warn, not os.Stderr).
	warnings := captureWarnings(t)
	origVersion := version.Version
	t.Cleanup(func() { version.Version = origVersion })

	version.Version = "stamp-A"
	endpoint, err := ServeRunnerMCP(testConfig(), "same-harp", testHome(t), false, "")
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	// Simulate THIS process having been rebuilt since the runner launched —
	// the runner's Implementation.Version was already captured as "stamp-A"
	// at ServeRunnerMCP construction time above; only this process's own
	// version.Version changes now.
	version.Version = "stamp-B"

	trigger := forwardTrigger{Kind: "env var", Name: "CTXLOOM_MCP_SOCKET"}
	var cs *mcp.ClientSession
	var outcome forwardOutcome
	captureStderr(t, func() {
		cs, outcome, err = prepareForward(context.Background(), trigger, endpoint.SocketPath)
	})
	require.NoError(t, err)
	assert.Nil(t, cs)
	assert.Equal(t, forwardOutcomeRefused, outcome)
	refusal := warnings.String()
	assert.Contains(t, refusal, "stamp-A")
	assert.Contains(t, refusal, "stamp-B")
}

// TestVerifyForwardTarget is the pure-function table test pinning
// verifyForwardTarget's exact decision surface, independent of any
// networking: both axes matching (or unset where there is nothing to
// compare) accept; either axis disagreeing refuses.
func TestVerifyForwardTarget(t *testing.T) {
	origVersion := version.Version
	t.Cleanup(func() { version.Version = origVersion })
	version.Version = "this-build"

	cases := []struct {
		name         string
		callerHarp   string
		runnerHarp   string
		runnerStamp  string
		wantAccepted bool
	}{
		{"matching harp, matching stamp: accepted", "h", "h", "this-build", true},
		{"caller has no harp expectation: accepted regardless of runner harp", "", "anything", "this-build", true},
		{"runner reports no harp: accepted (nothing to compare)", "h", "", "this-build", true},
		{"mismatched harp: refused", "h1", "h2", "this-build", false},
		{"matching harp, mismatched stamp: refused", "h", "h", "other-build", false},
		{"runner reports no stamp: accepted (nothing to compare)", "h", "h", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(agent.SessionHarpEnv, tc.callerHarp)
			reason := verifyForwardTarget(tc.runnerHarp, tc.runnerStamp)
			if tc.wantAccepted {
				assert.Empty(t, reason, "expected acceptance, got refusal reason %q", reason)
			} else {
				assert.NotEmpty(t, reason, "expected a refusal reason")
			}
		})
	}
}

// CAUTION FOR FUTURE TESTS: do not drive ServeStdio's real local-startup
// path (a REFUSED or absent forward falling through into ctxServer.startup)
// from this package. loadStartupConfig calls config.Load() directly with no
// injectable-loader seam (unlike loadStartupConfigWith), so a test cannot
// substitute a stub config — and a real resolved config here can enable
// runStartupSync's remote-reference walk, a live, unbounded network call.
// Measured directly: `go test -timeout 30s` on such a test had to be killed
// by its own timeout rather than finishing. Coverage for ServeStdio's
// forward-trigger/fall-through wiring stays at the safe layer instead:
// runMCPForward's forwardOutcome contract (this file) and
// probeWellKnownRunner's marker-path decisions (mcp_discovery_test.go) —
// the remaining glue in ServeStdio itself is a short, directly readable
// diff. A real integration test here needs loadStartupConfig to accept an
// injected loader first.
