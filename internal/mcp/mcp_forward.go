package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/mcpsocket"
	"github.com/ctxloom/ctxloom/internal/version"
)

// FORWARD MODE (agentcoord B1.6, runner-terminated MCP): when a `ctxloom mcp`
// process finds CTXLOOM_MCP_SOCKET in its harness-inherited env, it is the
// harness's stdio endpoint and the RUNNER owns the whole surface. This
// server then forwards ALL its tools (and resources) to the runner's MCP
// endpoint over HTTP-on-unix — a standard MCP stdio↔HTTP bridge, zero
// bespoke protocol. The socket is container-local, so the tool path never
// crosses the container boundary; the runner is the one credential holder
// and the one egress to the coordinator. (This REPLACED the B1
// forward-to-coordinator HTTP mode: CTXLOOM_COORD_URL/CRED are now consumed
// ONLY by the runner.)
//
// OFF-LINUX TCP FALLBACK: internal/acp/container_transport.go's ACP
// reach-back cannot always hand this a unix socket path — off Linux (macOS/
// Windows Docker Desktop) it bridges the runner's unix socket onto a host-
// loopback TCP port instead (a bind-mounted unix socket file is not a live
// endpoint across the Docker Desktop VM boundary) and encodes that as
// "tcp://host:port". Both ends read that marker from the same shared leaf
// (internal/shared/mcpsocket) so they cannot drift — and from a LEAF, not from
// internal/acp itself: a CLI frontend must never import the ACP client (the
// one-door invariant, internal/acptest's no-import test).

// dialReachBackSocket dials socketPath as either a unix socket (the default —
// any absolute filesystem path) or, when it carries the mcpsocket.TCPPrefix
// marker, a TCP host:port — the off-Linux ACP reach-back fallback. Factored
// out of runMCPForward's transport so the dial decision is independently
// unit-testable without driving a full stdio server.
func dialReachBackSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	if addr, ok := strings.CutPrefix(socketPath, mcpsocket.TCPPrefix); ok {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
}

// forwardTrigger names what caused ServeStdio to attempt a forward, so the
// mandatory pre-forward diagnostic (graceful-egomaniac unit 1: "forwarding
// is never silent") can say precisely which of the two independent trigger
// paths fired and by what name — the measured incident this fixes was THREE
// silent hijacks in one day, one via the env var and two via the cwd-keyed
// marker, with no diagnostic distinguishing them at all.
type forwardTrigger struct {
	// Kind is "env var" or "discovery marker" — human-readable, never parsed.
	Kind string
	// Name is the env var's name (coord.EnvMCPSocket) or the marker file's
	// absolute path, whichever Kind names.
	Name string
}

func (t forwardTrigger) String() string {
	return fmt.Sprintf("%s %s", t.Kind, t.Name)
}

// forwardOutcome is runMCPForward's verdict on what happened after a
// successful connect: forwardOutcomeServed means it ran the proxy (until the
// client disconnected or ctx was cancelled, or it hit a mid-flight error, in
// which case the error return says so); forwardOutcomeRefused means the
// unit-2 identity/stamp verification refused this target BEFORE any tool
// traffic crossed it, and ServeStdio must fall back to local startup — a
// refusal is not itself an error, so it always carries a nil error.
type forwardOutcome int

const (
	forwardOutcomeServed forwardOutcome = iota
	forwardOutcomeRefused
)

// runMCPForward serves the stdio proxy until the client disconnects or ctx
// is cancelled. An unreachable runner socket is a hard startup error: a
// silently-empty toolset would be a wrong-context session. A refused target
// (identity/stamp mismatch) is NOT a hard error — see forwardOutcome.
func runMCPForward(ctx context.Context, trigger forwardTrigger, socketPath string) (forwardOutcome, error) {
	cs, outcome, err := prepareForward(ctx, trigger, socketPath)
	if err != nil {
		return forwardOutcomeServed, err
	}
	if outcome == forwardOutcomeRefused {
		return forwardOutcomeRefused, nil
	}
	defer func() { _ = cs.Close() }()

	server, err := buildForwardServer(ctx, cs)
	if err != nil {
		return forwardOutcomeServed, err
	}
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return forwardOutcomeServed, err
	}
	return forwardOutcomeServed, nil
}

// prepareForward dials socketPath, completes the MCP initialize handshake,
// emits the unit-1 "forwarding is never silent" diagnostic, and runs the
// unit-2 identity/stamp verification — all BEFORE any tool call is ever
// proxied. Factored out of runMCPForward so both halves (the diagnostic +
// verification decision, and the actual stdio proxy loop) are independently
// testable: a test can drive this against a real runner without ever
// touching stdio, which would block on a real test process's stdin.
//
// On forwardOutcomeServed, cs is the live, verified session — the caller
// owns closing it. On forwardOutcomeRefused, cs is nil (already closed) and
// the caller must fall back to local startup. err is non-nil ONLY for a
// genuine connection failure (the pre-existing hard-error contract);
// a refusal is reported via outcome, never via err.
func prepareForward(ctx context.Context, trigger forwardTrigger, socketPath string) (cs *mcp.ClientSession, outcome forwardOutcome, err error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: version.Version}, nil)
	transport := &mcp.StreamableClientTransport{
		// The endpoint host is nominal — the transport dials socketPath via
		// dialReachBackSocket, either a unix socket (the default) or a TCP
		// host:port (the off-Linux reach-back fallback).
		Endpoint: "http://ctxloom-runner/mcp",
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialReachBackSocket(ctx, socketPath)
			},
		}},
	}
	cs, err = client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, forwardOutcomeServed, fmt.Errorf("ctxloom mcp (forward mode): connect runner at %s: %w", socketPath, err)
	}

	runnerHarp, runnerStamp := forwardTargetIdentity(cs)

	// UNIT 1 — forwarding is never silent: this line fires on EVERY forward
	// attempt that reaches a live connection, whether accepted or refused
	// below, naming what triggered it, the socket, and the target's claimed
	// identity — the diagnostic that was measured completely absent across
	// three silent hijacks (one env-var-triggered, two marker-triggered) in
	// one day.
	fmt.Fprintf(os.Stderr, "ctxloom: mcp forward: %s selected runner at %s (harp %q, build %q)\n",
		trigger, socketPath, runnerHarp, runnerStamp)

	// UNIT 2 — identity + stamp verification.
	if reason := verifyForwardTarget(runnerHarp, runnerStamp); reason != "" {
		clidiag.Warn("ctxloom", "mcp forward: refusing %s at %s — %s; falling back to a local session instead of forwarding", trigger, socketPath, reason)
		_ = cs.Close()
		return nil, forwardOutcomeRefused, nil
	}

	return cs, forwardOutcomeServed, nil
}

// forwardTargetIdentity reads the connected runner's self-reported session
// harp and build stamp from the MCP initialize handshake. The harp rides
// Implementation.Title — an existing, spec-legal field this internal
// client<->runner handshake repurposes (newRunnerMCPServer sets it; the
// stdio server this same file later exposes to the REAL external client,
// buildForwardServer, sets no Title at all, so nothing about the external
// wire protocol changes). The stamp rides Implementation.Version, which
// already carried version.Version before this fix — only reading it back is
// new.
func forwardTargetIdentity(cs *mcp.ClientSession) (harp, stamp string) {
	init := cs.InitializeResult()
	if init == nil || init.ServerInfo == nil {
		return "", ""
	}
	return init.ServerInfo.Title, init.ServerInfo.Version
}

// verifyForwardTarget returns "" when the connected runner is safe to
// forward to, or a human-readable reason otherwise. Each axis is checked
// only when THIS session has its own expectation for it:
//
//   - harp: compared against CTXLOOM_SESSION_HARP. Unset (a genuinely
//     standalone `ctxloom mcp serve`, or a harness that dropped the env the
//     same way it drops CTXLOOM_MCP_SOCKET) means there is nothing to
//     compare against, so this axis is silently skipped rather than guessed
//     — matching probeWellKnownRunner's identical hedge for the marker-level
//     pre-check (mcp_discovery.go).
//   - stamp: always checked, because this process always knows its own
//     version.Version — a forward from one binary revision to another is a
//     mixed-build session even when identity matches (e.g. a stale runner
//     left running across a `just build`), and mixed-build behaviour is not
//     something this fix wants to paper over silently.
func verifyForwardTarget(runnerHarp, runnerStamp string) string {
	if callerHarp := os.Getenv(agent.SessionHarpEnv); callerHarp != "" && runnerHarp != "" && callerHarp != runnerHarp {
		return fmt.Sprintf("session identity mismatch: this session is %q, the runner is %q", callerHarp, runnerHarp)
	}
	if runnerStamp != "" && runnerStamp != version.Version {
		return fmt.Sprintf("build stamp mismatch: this binary is %q, the runner is %q", version.Version, runnerStamp)
	}
	return ""
}

// buildForwardServer mirrors the runner session's tools and resources onto a
// local stdio server with passthrough handlers.
func buildForwardServer(ctx context.Context, cs *mcp.ClientSession) (*mcp.Server, error) {
	instructions := ""
	if init := cs.InitializeResult(); init != nil {
		instructions = init.Instructions
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: version.Version,
	}, &mcp.ServerOptions{Instructions: instructions})

	n, err := forwardTools(ctx, cs, server)
	if err != nil {
		return nil, err
	}
	// runMCPForward's own doc claims "an unreachable runner socket
	// is a hard startup error: a silently-empty toolset would be a wrong-
	// context session" — but nothing ever checked that ANY tool actually
	// got registered. A runner that connects fine but advertises zero
	// tools (the wrong-context shape the doc already names) used to stand
	// up a perfectly healthy-LOOKING, entirely empty forward server.
	if n == 0 {
		return nil, fmt.Errorf("ctxloom mcp (forward mode): the runner advertised zero tools — refusing to stand up an empty forwarded session (wrong context?)")
	}
	if err := forwardResources(ctx, cs, server); err != nil {
		return nil, err
	}
	return server, nil
}

func forwardTools(ctx context.Context, cs *mcp.ClientSession, server *mcp.Server) (int, error) {
	cursor := ""
	n := 0
	for {
		page, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return n, fmt.Errorf("ctxloom mcp (forward mode): list runner tools: %w", err)
		}
		for _, tool := range page.Tools {
			name := tool.Name
			server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return cs.CallTool(ctx, &mcp.CallToolParams{
					Name:      name,
					Arguments: json.RawMessage(req.Params.Arguments),
				})
			})
			n++
		}
		if page.NextCursor == "" {
			return n, nil
		}
		cursor = page.NextCursor
	}
}

func forwardResources(ctx context.Context, cs *mcp.ClientSession, server *mcp.Server) error {
	cursor := ""
	for {
		page, err := cs.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			// A runner without resources is fine; only a transport
			// fault matters — the SDK types both as errors, so this
			// degrades to tools-only rather than failing the whole
			// session, but the degrade used to be SILENT despite
			// the comment claiming "the error surfaced" — it never did.
			clidiag.Warn("ctxloom", "ctxloom mcp (forward mode): list runner resources: %v (degrading to tools-only)", err)
			return nil //nolint:nilerr // resources are optional surface
		}
		for _, res := range page.Resources {
			uri := res.URI
			server.AddResource(res, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				_ = req
				return cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
			})
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}
