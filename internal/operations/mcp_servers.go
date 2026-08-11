package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// MCPServerEntry represents an MCP server in operation results.
type MCPServerEntry struct {
	Name         string            `json:"name"`
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Backend      string            `json:"backend"`
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
	Servers      []MCPServerEntry `json:"servers"`
	Count        int              `json:"count"`
	AutoRegister bool             `json:"auto_register"`
}

// ListMCPServers returns all configured MCP servers.
func ListMCPServers(ctx context.Context, cfg *config.Config, req ListMCPServersRequest) (*ListMCPServersResult, error) {
	freshCfg, err := resolveListConfig(cfg)
	if err != nil {
		return nil, err
	}

	servers := collectMCPServers(freshCfg, strings.ToLower(req.Query))
	sortMCPServers(servers, req.SortBy, req.SortOrder)

	mcpCfg := freshCfg.GetMCPConfig()
	return &ListMCPServersResult{
		Servers:      servers,
		Count:        len(servers),
		AutoRegister: mcpCfg.ShouldAutoRegisterCtxloom(),
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

// mcpServerMatches reports whether a server matches the (already lower-cased)
// query against its name or command. An empty query matches everything.
func mcpServerMatches(query, name, command string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), query) ||
		strings.Contains(strings.ToLower(command), query)
}

// mcpEntry builds an MCPServerEntry for the given backend label.
func mcpEntry(name string, srv wire.MCPServer, backend string) MCPServerEntry {
	return MCPServerEntry{
		Name:         name,
		Command:      srv.Command,
		Args:         srv.Args,
		Env:          srv.Env,
		Backend:      backend,
		Notes:        srv.Notes,
		Installation: srv.Installation,
	}
}

// collectMCPServers gathers unified and backend-specific servers matching query.
func collectMCPServers(cfg *config.Config, query string) []MCPServerEntry {
	var servers []MCPServerEntry
	for name, srv := range cfg.GetMCPServers() {
		if mcpServerMatches(query, name, srv.Command) {
			servers = append(servers, mcpEntry(name, srv, "unified"))
		}
	}
	for backend, backendServers := range cfg.GetMCPPlugins() {
		for name, srv := range backendServers {
			if mcpServerMatches(query, name, srv.Command) {
				servers = append(servers, mcpEntry(name, srv, backend))
			}
		}
	}
	return servers
}

// sortMCPServers sorts servers in place by name or command. An empty sortBy
// defaults to name. An UNRECOGNISED sortBy also sorts by name, loudly: leaving
// the slice untouched would ship it in collectMCPServers' build order, which
// comes from Go map iteration over the unified and per-backend scopes, so the
// same input could come back ordered differently on the next call.
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

// GetMCPServerResult holds every scope the named server is configured in — the
// unified entry and/or per-backend entries. Found is false when no scope
// matches; Entries is then empty (never nil), so a json consumer always reads a
// list.
type GetMCPServerResult struct {
	Name    string           `json:"name"`
	Found   bool             `json:"found"`
	Entries []MCPServerEntry `json:"entries"`
}

// GetMCPServer returns the configured MCP server with the given exact name
// across the unified scope and any per-backend scopes it appears in. It is the
// single-name counterpart to ListMCPServers and reuses the same MCPServerEntry
// shape, so a frontend reads identical structure from both.
func GetMCPServer(ctx context.Context, cfg *config.Config, req GetMCPServerRequest) (*GetMCPServerResult, error) {
	freshCfg, err := resolveListConfig(cfg)
	if err != nil {
		return nil, err
	}

	entries := []MCPServerEntry{}
	if srv, ok := freshCfg.GetMCPServers()[req.Name]; ok {
		entries = append(entries, mcpEntry(req.Name, srv, "unified"))
	}
	for backend, backendServers := range freshCfg.GetMCPPlugins() {
		if srv, ok := backendServers[req.Name]; ok {
			entries = append(entries, mcpEntry(req.Name, srv, backend))
		}
	}
	// Map iteration over per-backend scopes is non-deterministic; sort by scope
	// for stable output (the unified entry sorts ahead of backend names).
	sort.Slice(entries, func(i, j int) bool { return entries[i].Backend < entries[j].Backend })

	return &GetMCPServerResult{
		Name:    req.Name,
		Found:   len(entries) > 0,
		Entries: entries,
	}, nil
}

// AddMCPServerRequest contains parameters for adding an MCP server.
type AddMCPServerRequest struct {
	Name         string   `json:"name"`
	Command      string   `json:"command"`
	Args         []string `json:"args"`
	Backend      string   `json:"backend"`      // unified, claude-code, codex
	Notes        string   `json:"notes"`        // Human-readable notes, not sent to AI
	Installation string   `json:"installation"` // Setup/installation instructions, not sent to AI
}

// AddMCPServerResult contains the result of adding an MCP server.
type AddMCPServerResult struct {
	Status  string `json:"status"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Backend string `json:"backend"`
	Message string `json:"message"` // Operational status message
}

// AddMCPServer adds a new MCP server configuration, inside one Manager.Update
// transaction: the duplicate-name check and the write happen against the same
// locked, freshly-reloaded Draft, so a concurrent writer can never slip a
// same-named server in between the check and the save.
func AddMCPServer(ctx context.Context, mgr *config.Manager, req AddMCPServerRequest) (*AddMCPServerResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}

	server := wire.MCPServer{
		Command:      req.Command,
		Args:         req.Args,
		Notes:        req.Notes,
		Installation: req.Installation,
	}

	unified := isUnifiedBackend(req.Backend)
	err := mgr.Update(func(d *config.Draft) error {
		if unified {
			return addUnifiedServer(d, req.Name, server)
		}
		return addBackendServer(d, req.Backend, req.Name, server)
	})
	if err != nil {
		return nil, err
	}

	scope := "unified"
	if !unified {
		scope = req.Backend
	}

	return &AddMCPServerResult{
		Status:  "added",
		Name:    req.Name,
		Command: req.Command,
		Backend: scope,
		Message: "Run apply_hooks to inject into backend settings",
	}, nil
}

// isUnifiedBackend reports whether a backend value targets the unified server
// map (empty or the explicit "unified" label).
func isUnifiedBackend(backend string) bool {
	return backend == "" || backend == "unified"
}

// addUnifiedServer inserts server into the draft's unified map, erroring if
// name already exists.
func addUnifiedServer(d *config.Draft, name string, server wire.MCPServer) error {
	if _, ok := d.MCP.Servers[name]; ok {
		return fmt.Errorf("MCP server %q already exists", name)
	}
	if d.MCP.Servers == nil {
		d.MCP.Servers = make(map[string]wire.MCPServer)
	}
	d.MCP.Servers[name] = server
	return nil
}

// addBackendServer inserts server into a backend's plugin map on the draft,
// erroring if name already exists for that backend.
func addBackendServer(d *config.Draft, backend, name string, server wire.MCPServer) error {
	if _, ok := d.MCP.Plugins[backend][name]; ok {
		return fmt.Errorf("MCP server %q already exists for backend %s", name, backend)
	}
	if d.MCP.Plugins == nil {
		d.MCP.Plugins = make(map[string]map[string]wire.MCPServer)
	}
	if d.MCP.Plugins[backend] == nil {
		d.MCP.Plugins[backend] = make(map[string]wire.MCPServer)
	}
	d.MCP.Plugins[backend][name] = server
	return nil
}

// RemoveMCPServerRequest contains parameters for removing an MCP server.
type RemoveMCPServerRequest struct {
	Name    string `json:"name"`
	Backend string `json:"backend"` // unified, claude-code, codex, or empty for all
}

// RemoveMCPServerResult contains the result of removing an MCP server.
type RemoveMCPServerResult struct {
	Status      string   `json:"status"`
	Name        string   `json:"name"`
	RemovedFrom []string `json:"removed_from"`
	Message     string   `json:"message"` // Operational status message
}

// RemoveMCPServer removes an MCP server configuration, inside one
// Manager.Update transaction: the "does it exist anywhere" scan and the
// deletes happen against the same locked, freshly-reloaded Draft, so a
// concurrent writer can never resurrect an entry between the scan and the
// save.
func RemoveMCPServer(ctx context.Context, mgr *config.Manager, req RemoveMCPServerRequest) (*RemoveMCPServerResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}

	var removedFrom []string
	err := mgr.Update(func(d *config.Draft) error {
		removedFrom = removeMCPServerEntries(d, req.Backend, req.Name)
		if len(removedFrom) == 0 {
			return fmt.Errorf("MCP server %q not found", req.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &RemoveMCPServerResult{
		Status:      "removed",
		Name:        req.Name,
		RemovedFrom: removedFrom,
		Message:     "Run apply_hooks to update backend settings",
	}, nil
}

// removeMCPServerEntries deletes the named server from the locations implied by
// backend, returning the list of locations it was removed from. An empty
// backend removes from the unified map and every plugin backend; "unified"
// removes only from the unified map; any other value removes only from that
// specific backend (the unified map is checked only for "" / "unified").
func removeMCPServerEntries(d *config.Draft, backend, name string) []string {
	var removedFrom []string
	if isUnifiedBackend(backend) && removeUnifiedServer(d, name) {
		removedFrom = append(removedFrom, "unified")
	}

	switch {
	case backend == "":
		removedFrom = append(removedFrom, removeFromAllBackends(d, name)...)
	case backend != "unified":
		if removeBackendServer(d, backend, name) {
			removedFrom = append(removedFrom, backend)
		}
	}
	return removedFrom
}

// removeUnifiedServer deletes name from the draft's unified map, reporting
// whether it was present.
func removeUnifiedServer(d *config.Draft, name string) bool {
	if _, ok := d.MCP.Servers[name]; !ok {
		return false
	}
	delete(d.MCP.Servers, name)
	return true
}

// removeBackendServer deletes name from a specific backend on the draft,
// reporting whether it was present.
func removeBackendServer(d *config.Draft, backend, name string) bool {
	servers, ok := d.MCP.Plugins[backend]
	if !ok {
		return false
	}
	if _, ok := servers[name]; !ok {
		return false
	}
	delete(servers, name)
	return true
}

// removeFromAllBackends deletes name from every plugin backend on the draft,
// returning the backends it was removed from.
func removeFromAllBackends(d *config.Draft, name string) []string {
	var removed []string
	for backend, servers := range d.MCP.Plugins {
		if _, ok := servers[name]; ok {
			delete(servers, name)
			removed = append(removed, backend)
		}
	}
	return removed
}

// SetMCPAutoRegisterRequest contains parameters for setting auto-register.
type SetMCPAutoRegisterRequest struct {
	Enabled bool `json:"enabled"`
}

// SetMCPAutoRegisterResult contains the result of setting auto-register.
type SetMCPAutoRegisterResult struct {
	Status       string `json:"status"`
	AutoRegister bool   `json:"auto_register"`
	Message      string `json:"message"` // Operational status message
}

// SetMCPAutoRegister enables or disables auto-registration of ctxloom's MCP
// server, inside one Manager.Update transaction.
func SetMCPAutoRegister(ctx context.Context, mgr *config.Manager, req SetMCPAutoRegisterRequest) (*SetMCPAutoRegisterResult, error) {
	if mgr == nil {
		return nil, fmt.Errorf("manager is required")
	}
	enabled := req.Enabled
	if err := mgr.Update(func(d *config.Draft) error {
		d.MCP.AutoRegisterCtxloom = &enabled
		return nil
	}); err != nil {
		return nil, err
	}

	return &SetMCPAutoRegisterResult{
		Status:       "updated",
		AutoRegister: req.Enabled,
		Message:      "Run apply_hooks to update backend settings",
	}, nil
}
