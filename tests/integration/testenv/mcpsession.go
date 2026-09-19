package testenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// MCPSession is a go-sdk client session to an MCP server this harness spawned
// over stdio. It is the single shared door for mock-agent test traffic:
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
	pid int // the spawned server, for Close's plugin-child reap
}

// StartMCP spawns the MCP server in the project directory with the isolated
// (and session-scrubbed — see isolatedEnv) environment and completes the
// handshake. extraEnv entries ("KEY=VALUE") are appended last and win over
// the isolated environment.
//
// The argv is agent.CtxloomMCPArgs, the SAME value ctxloom writes into every
// engine's own MCP settings, so this harness drives the exact invocation an
// engine drives. Spelling it out here instead would let the two drift, and a
// harness that speaks the protocol to a spelling no engine uses proves
// nothing about what engines get.
func (e *TestEnvironment) StartMCP(extraEnv ...string) (*MCPSession, error) {
	return e.StartMCPFrom(e.AppBinary, extraEnv...)
}

// StartMCPFrom is StartMCP with the server binary named by the caller instead
// of AppBinary: same argv, same directory, same isolated environment. It
// exists for a scenario that must run the coordinator from a binary it
// controls the lifetime of (a copy it can unlink while the process lives),
// which AppBinary — shared by every scenario in the run — can never be.
func (e *TestEnvironment) StartMCPFrom(bin string, extraEnv ...string) (*MCPSession, error) {
	return connectMCP(bin, agent.CtxloomMCPArgs, e.ProjectDir, e.isolatedEnv(), extraEnv...)
}

// connectMCP spawns bin(args...) as an MCP server over stdio in dir, with
// baseEnv plus extraEnv ("KEY=VALUE", appended last so it wins), and returns
// the initialized session. Shared by StartMCP and StartTaskloomMCP so a
// second binary gets the exact same process and transport plumbing.
func connectMCP(bin string, args []string, dir string, baseEnv []string, extraEnv ...string) (*MCPSession, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(baseEnv, extraEnv...)
	cmd.Stderr = os.Stderr // surface server warnings to the test log
	// See pdeathsig_linux.go: this MCP server can itself delegate to an
	// agent (agent_run) that spawns an "llm serve <label>" plugin
	// subprocess (the same self-invoking path `ctxloom run` uses), so it
	// gets the same "die with the test binary" guarantee `ctxloom run`
	// invocations do.
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
// parent never runs its own cleanup — so any "llm serve <label>" plugin
// subprocess it spawned via agent delegation (setsid'd into its own session,
// unreachable by anything this harness could signal as a group) is captured
// by pid BEFORE the close and explicitly reaped afterward, the same
// defense-in-depth as ptyrun.go's PTYSession.Close and for the identical
// reason.
//
// The server's exit status is returned: a server that exits non-zero on
// stdin EOF is a finding, not noise. Nil-safe, so a scenario that never
// opened a session can close it unconditionally.
func (s *MCPSession) Close() error {
	if s == nil {
		return nil
	}
	children := PluginChildrenOf(s.pid)
	err := s.ClientSession.Close()
	KillPids(children)
	return err
}
