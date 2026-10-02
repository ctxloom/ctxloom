package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// NewDocServer builds an MCP server with the full tool + resource surface
// registered but no live config, for documentation generation (scripts/gendocs).
//
// It mirrors GetRootCmd(): the cobra tree is the single source of truth for the
// CLI reference, and this server is the single source of truth for the MCP
// reference. The documented surface is the session endpoint every engine
// dials: the proto-canonical generated coordination tools, the cell-local
// tools over the loadout, and the host-relayed session tools. Registration
// only reads static tool literals and the embedded generated schemas — no
// handler is ever invoked and nothing is dialed, so an empty loadout and a
// dead coordinator endpoint are safe. gendocs enumerates the registered
// tools/resources via an in-memory MCP client (the SDK exposes no direct
// ListTools accessor on the server).
//
// The returned closer releases the runner.Home standing behind the surface. That
// Home is NOT inert: constructing one opens a gRPC client and dispatches two
// background loops that go on redialling the dead endpoint, so a caller that
// never closes it leaks a connection and two goroutines for the life of the
// process — and both list helpers below build a fresh server per call.
//
// Failures are returned rather than panicked. Nothing here is a user condition
// (the endpoint is a constant; a routing-table mismatch is a build defect), but
// both callers already report errors, and a panic in a completeness gate takes
// the whole test binary down instead of failing one assertion with a message.
func NewDocServer() (server *mcp.Server, closeHome func(), err error) {
	home, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL:     "http://127.0.0.1:1/mcp", // never dialed successfully; docgen only reads registrations
		Token:   "docgen",
		Harness: "docgen",
		Version: version.Version,
		// A harp names the run's spool; nothing is ever written to it here
		// (writers are lazy and a sweep of a directory that does not exist
		// is a no-op), but a run with no harp is refused.
		Harp: "docgen",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("docgen: dead-endpoint home: %w", err)
	}
	closeHome = func() { home.Close(0, "") }
	server, err = NewServer(report.To(nil), home, "", "", false, loadoutSurface{}, NewWakeSignal(nil)) // full surface documented, never the leaf-gated subset
	if err != nil {
		closeHome()
		return nil, nil, fmt.Errorf("docgen: assemble runner MCP surface: %w", err)
	}
	return server, closeHome, nil
}

// DocSurface is the documented MCP surface as a client enumerates it: the
// tool names, the resource URIs and the resource-template URIs, each sorted.
type DocSurface struct {
	Tools     []string
	Resources []string
	Templates []string
}

// ListDocSurface enumerates the documented MCP surface built by NewDocServer
// via an in-memory client round trip — the SDK exposes no direct accessor on
// the server itself. It exists so a completeness gate can measure the SAME
// surface this package documents: the one a real engine dials, in a session.
func ListDocSurface(ctx context.Context) (DocSurface, error) {
	server, closeHome, err := NewDocServer()
	if err != nil {
		return DocSurface{}, err
	}
	defer closeHome()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		return DocSurface{}, fmt.Errorf("connect doc server: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "completeness-check"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		return DocSurface{}, fmt.Errorf("connect doc client: %w", err)
	}
	defer cs.Close()

	var out DocSurface
	// The cursor-following iterators, so a server that pages its surface is
	// enumerated whole rather than to its first page.
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return DocSurface{}, fmt.Errorf("list tools: %w", err)
		}
		out.Tools = append(out.Tools, tool.Name)
	}
	for res, err := range cs.Resources(ctx, nil) {
		if err != nil {
			return DocSurface{}, fmt.Errorf("list resources: %w", err)
		}
		out.Resources = append(out.Resources, res.URI)
	}
	for tmpl, err := range cs.ResourceTemplates(ctx, nil) {
		if err != nil {
			return DocSurface{}, fmt.Errorf("list resource templates: %w", err)
		}
		out.Templates = append(out.Templates, tmpl.URITemplate)
	}
	sort.Strings(out.Tools)
	sort.Strings(out.Resources)
	sort.Strings(out.Templates)
	return out, nil
}

// ListDocToolNames returns the sorted tool names of the documented surface.
func ListDocToolNames(ctx context.Context) ([]string, error) {
	surface, err := ListDocSurface(ctx)
	if err != nil {
		return nil, err
	}
	return surface.Tools, nil
}

// ToolContract is one tool exactly as an MCP client receives it: description
// plus BOTH schemas, as raw JSON.
type ToolContract struct {
	Name         string
	Description  string
	InputSchema  string
	OutputSchema string
}

// ListDocToolContracts returns the full advertised contract of every tool on
// the runner-terminated MCP surface, via the same in-memory client round trip
// ListDocToolNames uses.
//
// It exists because the OUTPUT schema is where a coordination tool's result
// SHAPE is advertised, and a change to the proto-canonical shape a real
// harness is told to expect must be observable without a live session.
//
// Registration reads only static tool literals and the embedded generated
// schemas, so this dials nothing and invokes no handler.
func ListDocToolContracts(ctx context.Context) ([]ToolContract, error) {
	server, closeHome, err := NewDocServer()
	if err != nil {
		return nil, err
	}
	defer closeHome()
	return ToolContracts(ctx, server)
}

// ToolContracts enumerates a server's advertised tools through an in-memory
// client round trip — the SDK exposes no direct accessor on the server itself —
// and returns them sorted by name.
func ToolContracts(ctx context.Context, server *mcp.Server) ([]ToolContract, error) {
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
	out := make([]ToolContract, 0, len(res.Tools))
	for _, t := range res.Tools {
		c := ToolContract{Name: t.Name, Description: t.Description}
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
