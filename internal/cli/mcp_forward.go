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
// "tcp://host:port". reachBackTCPPrefix is duplicated from that file's
// identical constant — same import-cycle reason mcpSocketEnvVar/EnvMCPSocket
// is duplicated between internal/acp and agentcoord/coord (see its doc):
// keep both literals in sync by hand.
const reachBackTCPPrefix = "tcp://"

// dialReachBackSocket dials socketPath as either a unix socket (the default —
// any absolute filesystem path) or, when it carries the reachBackTCPPrefix
// marker, a TCP host:port — the off-Linux ACP reach-back fallback. Factored
// out of runMCPForward's transport so the dial decision is independently
// unit-testable without driving a full stdio server.
func dialReachBackSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	if addr, ok := strings.CutPrefix(socketPath, reachBackTCPPrefix); ok {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
}

// runMCPForward serves the stdio proxy until the client disconnects or ctx
// is cancelled. An unreachable runner socket is a hard startup error: a
// silently-empty toolset would be a wrong-context session.
func runMCPForward(ctx context.Context, socketPath string) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: Version}, nil)
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
		Version: Version,
	}, &mcp.ServerOptions{Instructions: instructions})

	if err := forwardTools(ctx, cs, server); err != nil {
		return nil, err
	}
	if err := forwardResources(ctx, cs, server); err != nil {
		return nil, err
	}
	return server, nil
}

func forwardTools(ctx context.Context, cs *mcp.ClientSession, server *mcp.Server) error {
	cursor := ""
	for {
		page, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("ctxloom mcp (forward mode): list runner tools: %w", err)
		}
		for _, tool := range page.Tools {
			name := tool.Name
			server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return cs.CallTool(ctx, &mcp.CallToolParams{
					Name:      name,
					Arguments: json.RawMessage(req.Params.Arguments),
				})
			})
		}
		if page.NextCursor == "" {
			return nil
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
			// fault matters — but the SDK types both as errors, so degrade
			// to tools-only with the error surfaced.
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
