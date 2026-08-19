package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MCPServerEntry represents an MCP server in operation results.
//
// Source names the bundle that ships it (wire.MCPServer.SCM's
// "bundle:<identity>"), which is also its trust identity: every MCP server a
// session registers comes from a bundle, so `ctxloom bundle trust|reject
// <bundle>#mcp/<name>` addresses any entry listed here.
type MCPServerEntry struct {
	Name         string            `json:"name"`
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Source       string            `json:"source"`
	Notes        string            `json:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string            `json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
}

// ListMCPServersRequest contains parameters for listing MCP servers.
type ListMCPServersRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // name, command
	SortOrder string `json:"sort_order"` // asc, desc
}

// ListMCPServersResult contains the list of MCP servers.
type ListMCPServersResult struct {
	Servers []MCPServerEntry `json:"servers"`
	Count   int              `json:"count"`
}

// ListMCPServers returns the MCP servers this project registers: the set
// Config.ResolveBundleMCPServers resolves for the configured default profiles
// — builtin bundles (ctxloom's own server among them), each discovered
// companion's loadout, and the profile→bundle cascade — which is the same set
// the settings writers materialize.
func ListMCPServers(ctx context.Context, cfg *config.Config, req ListMCPServersRequest) (*ListMCPServersResult, error) {
	freshCfg, err := resolveListConfig(cfg)
	if err != nil {
		return nil, err
	}

	servers := collectMCPServers(freshCfg, strings.ToLower(req.Query))
	sortMCPServers(servers, req.SortBy, req.SortOrder)

	return &ListMCPServersResult{
		Servers: servers,
		Count:   len(servers),
	}, nil
}

// resolveListConfig returns cfg when the caller already has one loaded, or
// loads a fresh one when cfg is nil.
func resolveListConfig(cfg *config.Config) (*config.Config, error) {
	if cfg != nil {
		return cfg, nil
	}
	freshCfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return freshCfg, nil
}

// registeredMCPServers resolves the server set a settings writer would
// materialize for the configured default profiles, with ctxloom's own entry
// carrying the command that actually reaches a surface rather than the bare
// name its bundle declares (agent.ResolveManagedMCPServers). A listing that
// showed the bundle's literal would disagree with every engine's settings file
// and with `ctxloom doctor`'s MCP-invocation check. The override is empty: a
// listing is not a cell, so it reports the host resolution.
func registeredMCPServers(cfg *config.Config) map[string]wire.MCPServer {
	return agent.ResolveManagedMCPServers(cfg.ResolveBundleMCPServers(nil), "")
}

// mcpServerMatches reports whether a server matches the (already lower-cased)
// query against its name or command. An empty query matches everything.
func mcpServerMatches(query, name, command string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), query) ||
		strings.Contains(strings.ToLower(command), query)
}

// mcpEntry builds an MCPServerEntry from a resolved bundle server.
func mcpEntry(name string, srv wire.MCPServer) MCPServerEntry {
	return MCPServerEntry{
		Name:         name,
		Command:      srv.Command,
		Args:         srv.Args,
		Env:          srv.Env,
		Source:       strings.TrimPrefix(srv.SCM, "bundle:"),
		Notes:        srv.Notes,
		Installation: srv.Installation,
	}
}

// collectMCPServers gathers the registered servers matching query.
func collectMCPServers(cfg *config.Config, query string) []MCPServerEntry {
	var servers []MCPServerEntry
	for name, srv := range registeredMCPServers(cfg) {
		if mcpServerMatches(query, name, srv.Command) {
			servers = append(servers, mcpEntry(name, srv))
		}
	}
	return servers
}

// sortMCPServers sorts servers in place by name or command. An empty sortBy
// defaults to name. An UNRECOGNISED sortBy also sorts by name, loudly: leaving
// the slice untouched would ship it in collectMCPServers' build order, which
// comes from Go map iteration, so the same input could come back ordered
// differently on the next call.
func sortMCPServers(servers []MCPServerEntry, sortBy, sortOrder string) {
	reverse := sortOrder == "desc"
	less := func(a, b string) bool {
		cmp := strings.Compare(strings.ToLower(a), strings.ToLower(b))
		if reverse {
			return cmp > 0
		}
		return cmp < 0
	}
	byName := func() {
		sort.Slice(servers, func(i, j int) bool { return less(servers[i].Name, servers[j].Name) })
	}
	switch sortBy {
	case "", "name":
		byName()
	case "command":
		sort.Slice(servers, func(i, j int) bool { return less(servers[i].Command, servers[j].Command) })
	default:
		clidiag.Warn("ctxloom", "unknown sort_by %q for mcp servers; sorting by name (accepted: name, command)", sortBy)
		byName()
	}
}

// GetMCPServerRequest identifies the server to look up by exact name.
type GetMCPServerRequest struct {
	Name string `json:"name"`
}

// GetMCPServerResult holds the named server when the resolved set carries it.
// Found is false when no server matches; Entries is then empty (never nil), so
// a json consumer always reads a list.
type GetMCPServerResult struct {
	Name    string           `json:"name"`
	Found   bool             `json:"found"`
	Entries []MCPServerEntry `json:"entries"`
}

// GetMCPServer returns the registered MCP server with the given exact name. It
// is the single-name counterpart to ListMCPServers and reuses the same
// MCPServerEntry shape, so a frontend reads identical structure from both.
// Entries holds at most one server: one name resolves to one server, because
// ResolveBundleMCPServers has already collapsed every source into one map.
func GetMCPServer(ctx context.Context, cfg *config.Config, req GetMCPServerRequest) (*GetMCPServerResult, error) {
	freshCfg, err := resolveListConfig(cfg)
	if err != nil {
		return nil, err
	}

	entries := []MCPServerEntry{}
	if srv, ok := registeredMCPServers(freshCfg)[req.Name]; ok {
		entries = append(entries, mcpEntry(req.Name, srv))
	}

	return &GetMCPServerResult{
		Name:    req.Name,
		Found:   len(entries) > 0,
		Entries: entries,
	}, nil
}
