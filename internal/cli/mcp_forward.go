package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// runMCPForward serves the stdio proxy until the client disconnects or ctx
// is cancelled. An unreachable runner socket is a hard startup error: a
// silently-empty toolset would be a wrong-context session.
func runMCPForward(ctx context.Context, socketPath string) error {
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
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("ctxloom mcp (forward mode): connect runner at %s: %w", socketPath, err)
	}
	defer func() { _ = cs.Close() }()

	server, err := buildForwardServer(ctx, cs)
	if err != nil {
		return err
	}
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
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
