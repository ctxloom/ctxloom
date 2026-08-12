package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/version"
)

// NewDocMCPServer builds an MCP server with the full tool + resource surface
// registered but no live config, for documentation generation (scripts/gendocs).
//
// It mirrors GetRootCmd(): the cobra tree is the single source of truth for the
// CLI reference, and this server is the single source of truth for the MCP
// reference. Since B1.6 the documented surface is the RUNNER-terminated one —
// the mainline every harness sees through its stdio shim: the proto-canonical
// generated coordination tools (agent_run/send/recv/stop, roster,
// agent_report), the cell-local content tools, and the host-relay session
// tools. Registration only reads static tool literals and the embedded
// generated schemas — no handler is ever invoked and nothing is dialed, so a
// nil config and a dead coordinator endpoint are safe. gendocs enumerates the
// registered tools/resources via an in-memory MCP client (the SDK exposes no
// direct ListTools accessor on the server).
//
// The returned closer releases the coord.Home standing behind the surface. That
// Home is NOT inert: constructing one opens a gRPC client and dispatches two
// background loops that go on redialling the dead endpoint, so a caller that
// never closes it leaks a connection and two goroutines for the life of the
// process — and both list helpers below build a fresh server per call.
//
// Failures are returned rather than panicked. Nothing here is a user condition
// (the endpoint is a constant; a routing-table mismatch is a build defect), but
// both callers already report errors, and a panic in a completeness gate takes
// the whole test binary down instead of failing one assertion with a message.
func NewDocMCPServer() (server *mcp.Server, closeHome func(), err error) {
	home, err := coord.NewHome(context.Background(), coord.HomeConfig{
		URL:     "http://127.0.0.1:1/mcp", // never dialed successfully; docgen only reads registrations
		Token:   "docgen",
		Harness: "docgen",
		Version: version.Version,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("docgen: dead-endpoint home: %w", err)
	}
	closeHome = func() { home.Close(0, "") }
	server, err = newRunnerMCPServer(nil, "", home, false, "") // full surface documented, never the leaf-gated subset
	if err != nil {
		closeHome()
		return nil, nil, fmt.Errorf("docgen: assemble runner MCP surface: %w", err)
	}
	return server, closeHome, nil
}

// ListDocMCPToolNames returns the sorted tool names registered on the
// documented (runner-terminated) MCP surface built by NewDocMCPServer, via an
// in-memory client round trip -- the SDK exposes no direct accessor on the
// server itself (see internal/docsgen/mcp.go's enumerateMCPSurface, which
// does the identical round trip to render the published reference page).
//
// This exists so a completeness gate can measure the SAME surface
// this package documents. Before it, tests/acceptance/completeness_test.go
// only ever enumerated the STANDALONE `ctxloom mcp serve` surface (a
// deliberately reduced agent-delegation surface with different schemas --
// see mcpIntro's own caution block), so the surface a real harness actually
// talks to through `ctxloom run`/`ctxloom acp` had zero completeness
// coverage: roster, agent_report and agent_fetch_artifact are named in this
// package's own doc as existing only here, and were never checked by
// anything.
func ListDocMCPToolNames(ctx context.Context) ([]string, error) {
	server, closeHome, err := NewDocMCPServer()
	if err != nil {
		return nil, err
	}
	defer closeHome()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		return nil, fmt.Errorf("connect doc server: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "completeness-check"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return nil, fmt.Errorf("connect doc client: %w", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, t := range res.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names, nil
}

// DocMCPToolContract is one tool exactly as an MCP client receives it from the
// runner-terminated surface: description plus BOTH schemas, as raw JSON.
type DocMCPToolContract struct {
	Name         string
	Description  string
	InputSchema  string
	OutputSchema string
}

// ListDocMCPToolContracts returns the full advertised contract of every tool on
// the runner-terminated MCP surface, via the same in-memory client round trip
// ListDocMCPToolNames uses.
//
// It exists because the OUTPUT schema is where a coordination tool's result
// SHAPE is advertised, and nothing could observe it. `tests/acceptance`'s only
// MCP client is a `ctxloom mcp serve` subprocess, whose agent-delegation surface
// is a deliberately reduced one with DIFFERENT, hand-written schemas (see
// mcpIntro's caution block and mcp_tools_agents.go) — so a change to the
// proto-canonical shape a real harness is told to expect was invisible to every
// black-box test in the repo. agent_recv is the case that forced it: its result
// shape is a projection of PeerMessage, it has no hand-written twin on this
// surface, and plane-2 §4.B changes it.
//
// Registration reads only static tool literals and the embedded generated
// schemas, so this dials nothing and invokes no handler.
func ListDocMCPToolContracts(ctx context.Context) ([]DocMCPToolContract, error) {
	server, closeHome, err := NewDocMCPServer()
	if err != nil {
		return nil, err
	}
	defer closeHome()
	return toolContracts(ctx, server)
}

// NewStandaloneMCPServer builds the STANDALONE `ctxloom mcp serve` tool surface
// — the legacy stdio one a harness gets when it registers `ctxloom mcp serve`
// itself rather than reaching the runner through the stdio shim. It is the
// same registration `ctxloom mcp serve` performs (registerTools) over a config
// -less ctxServer, so nothing dials and no handler runs.
//
// It exists so the difference between this surface and the DOCUMENTED
// (runner-terminated) one built by NewDocMCPServer is measurable. The MCP
// reference page's intro states that difference as fact — which tools the
// standalone surface lacks, and which of the shared ones take different
// parameters — and nothing could check it, so the prose could only ever be
// verified by hand and could go stale without a single test noticing.
func NewStandaloneMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: version.Version}, nil)
	(&ctxServer{}).registerTools(server)
	return server
}

// ListStandaloneMCPToolContracts returns the full advertised contract of every
// tool on the standalone `ctxloom mcp serve` surface, via the same in-memory
// client round trip ListDocMCPToolContracts uses against the documented one.
func ListStandaloneMCPToolContracts(ctx context.Context) ([]DocMCPToolContract, error) {
	return toolContracts(ctx, NewStandaloneMCPServer())
}

// toolContracts enumerates a server's advertised tools through an in-memory
// client round trip — the SDK exposes no direct accessor on the server itself —
// and returns them sorted by name.
func toolContracts(ctx context.Context, server *mcp.Server) ([]DocMCPToolContract, error) {
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		return nil, fmt.Errorf("connect doc server: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "contract-check"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return nil, fmt.Errorf("connect doc client: %w", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	out := make([]DocMCPToolContract, 0, len(res.Tools))
	for _, t := range res.Tools {
		c := DocMCPToolContract{Name: t.Name, Description: t.Description}
		if t.InputSchema != nil {
			raw, merr := json.Marshal(t.InputSchema)
			if merr != nil {
				return nil, fmt.Errorf("marshal %s input schema: %w", t.Name, merr)
			}
			c.InputSchema = string(raw)
		}
		if t.OutputSchema != nil {
			raw, merr := json.Marshal(t.OutputSchema)
			if merr != nil {
				return nil, fmt.Errorf("marshal %s output schema: %w", t.Name, merr)
			}
			c.OutputSchema = string(raw)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
