package testenv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// MCPCallTimeout bounds one request/response round trip through an
// MCPSession. Handlers respond in milliseconds; the window is generous so the
// suite stays reliable on slow CI without hanging a broken server
// indefinitely — a wedged-but-alive server (stdout open, nothing arriving)
// times the call out instead of hanging the suite. Callers derive a context
// from it per call; the session itself imposes no deadline.
const MCPCallTimeout = 15 * time.Second

// mcpClientImpl is what the harness announces as its clientInfo.
var mcpClientImpl = &mcp.Implementation{Name: "ctxloom-acceptance", Version: "0"}

// MCPSession is a go-sdk client session to an MCP server: one this harness
// spawned over stdio, or a session's bound endpoint dialed over Streamable
// HTTP with its bearer (ConnectMCPEndpoint) — the way an engine reaches its
// runner. It is the single shared door for mock-agent test traffic:
// integration and acceptance suites speak to the server through the SDK's
// own ClientSession (embedded, so every request method is the SDK's) rather
// than a hand-rolled wire layer. The SDK client is the standard ctxloom's own
// servers are built on, and it is what a real engine drives; a harness
// speaking its own dialect proved nothing about that and carried its own
// handshake and framing defects.
//
// A returned session is connected AND initialized: mcp.Client.Connect
// performs the initialize / notifications/initialized handshake, fails on a
// JSON-RPC error answer, and negotiates the SDK's latest protocol version.
// InitializeResult() is what the server advertised (instructions included).
//
// MCPSession is single-goroutine as used here; the SDK session tolerates
// more, but the acceptance World that owns it is per-scenario and sequential.
type MCPSession struct {
	*mcp.ClientSession
	pid int // the spawned server, for Close's plugin-child reap; 0 for a dialed endpoint
}

// ErrNoEndpoint refuses to dial a session record that names no endpoint or
// carries no bearer: nothing serves such a session, and a dial would only
// produce a 401 that names nothing.
var ErrNoEndpoint = errors.New("testenv: the session names no MCP endpoint to dial")

// bearerTransport is the harness's http.RoundTripper: the session's bearer
// on every request, exactly what an engine's MCP client sends.
type bearerTransport struct {
	credential string
	next       http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.credential)
	return b.next.RoundTrip(r)
}

// ConnectMCPEndpoint dials a session's bound MCP endpoint — the URL and
// bearer its runner serves (runner/interaction.Endpoint) — over Streamable HTTP and
// completes the handshake. It is how a scenario reaches a standing session
// owner's coordination surface: the same door the owner's engine uses, with
// no shim process between.
func ConnectMCPEndpoint(ep sessions.Endpoint) (*MCPSession, error) {
	if ep.URL == "" || ep.Credential == "" {
		return nil, ErrNoEndpoint
	}
	client := &http.Client{Transport: bearerTransport{credential: ep.Credential, next: http.DefaultTransport}}
	ctx, cancel := context.WithTimeout(context.Background(), MCPCallTimeout)
	defer cancel()
	session, err := mcp.NewClient(mcpClientImpl, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ep.URL, HTTPClient: client}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect the session endpoint %s: %w", ep.URL, err)
	}
	return &MCPSession{ClientSession: session}, nil
}

// connectMCP spawns bin(args...) as an MCP server over stdio in dir, with
// baseEnv plus extraEnv ("KEY=VALUE", appended last so it wins), and returns
// the initialized session. ctxloom itself has no stdio server — a session's
// tools are dialed at its endpoint (ConnectMCPEndpoint) — so this is the
// companions' plumbing (StartTaskloomMCP).
func connectMCP(bin string, args []string, dir string, baseEnv []string, extraEnv ...string) (*MCPSession, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(baseEnv, extraEnv...)
	cmd.Stderr = os.Stderr // surface server warnings to the test log
	// See pdeathsig_linux.go: the same "die with the test binary"
	// guarantee `ctxloom run` invocations get.
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	ctx, cancel := context.WithTimeout(context.Background(), MCPCallTimeout)
	defer cancel()
	session, err := mcp.NewClient(mcpClientImpl, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp server %s: %w", bin, err)
	}
	return &MCPSession{ClientSession: session, pid: cmd.Process.Pid}, nil
}

// Close terminates the server subprocess. The SDK's CommandTransport closes
// stdin (the server's shutdown signal) and WAITS for a graceful exit before
// escalating to SIGTERM and then SIGKILL. The wait is load-bearing beyond
// kindness: a process killed by signal never runs its exit handlers, and a
// coverage-instrumented binary writes its counters in one of them, so a
// server killed mid-shutdown reports 0% coverage for every scenario it
// served.
//
// The escalation can still hard-kill a server that ignores EOF, and a killed
// parent never runs its own cleanup — so any subprocess it spawned (setsid'd
// into its own session, unreachable by anything this harness could signal as
// a group) is captured by pid BEFORE the close and explicitly reaped
// afterward, the same defense-in-depth as ptyrun.go's PTYSession.Close and
// for the identical reason.
//
// The server's exit status is returned: a server that exits non-zero on
// stdin EOF is a finding, not noise. Nil-safe, so a scenario that never
// opened a session can close it unconditionally.
func (s *MCPSession) Close() error {
	if s == nil {
		return nil
	}
	if s.pid == 0 {
		// A dialed endpoint: no subprocess of this harness's to reap.
		return s.ClientSession.Close()
	}
	children := PluginChildrenOf(s.pid)
	err := s.ClientSession.Close()
	KillPids(children)
	return err
}
