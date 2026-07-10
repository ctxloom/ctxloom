package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FORWARD MODE (agentcoord Wave B1, deliverable 4): when a `ctxloom mcp`
// process finds CTXLOOM_COORD_URL (+CTXLOOM_COORD_CRED) in its
// harness-inherited env, it is a spawned child's (or hosted parent's) stdio
// endpoint and the coordinator process owns everything. This server then
// forwards ALL its tools (and resources) to the coordinator's streamable-HTTP
// MCP endpoint with the credential — a standard MCP stdio↔HTTP bridge, zero
// bespoke protocol. Identity (harp, depth, project) derives from the
// credential per request on the coordinator side; nothing here reads this
// process's env as identity. This is how a CONTAINERIZED child's agent_send
// finally reaches its parent (the blue-paper fix).

// bearerRoundTripper stamps the credential on every forwarded HTTP request.
type bearerRoundTripper struct {
	token string
	next  http.RoundTripper
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(clone)
}

// runMCPForward serves the stdio proxy until the client disconnects or ctx
// is cancelled. A missing credential or unreachable coordinator is a hard
// startup error: a silently-empty toolset would be a wrong-context session.
func runMCPForward(ctx context.Context, url, cred string) error {
	if cred == "" {
		return fmt.Errorf("ctxloom mcp (forward mode): %s is set but %s is empty — the coordinator credential must ride the harness-inherited process env", "CTXLOOM_COORD_URL", "CTXLOOM_COORD_CRED")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ctxloom-forward", Version: Version}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: &bearerRoundTripper{token: cred, next: http.DefaultTransport}},
	}
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("ctxloom mcp (forward mode): connect coordinator at %s: %w", url, err)
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

// buildForwardServer mirrors the coordinator session's tools and resources
// onto a local stdio server with passthrough handlers.
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
			return fmt.Errorf("ctxloom mcp (forward mode): list coordinator tools: %w", err)
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
			// A coordinator without resources is fine; only a transport
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
