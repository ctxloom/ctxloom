package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/shared/wire"
)

// MCPServerEntry represents an MCP server in operation results.
type MCPServerEntry struct {
	Name         string   `json:"name"`
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	Backend      string   `json:"backend"`
	Notes        string   `json:"notes,omitempty"`        // Human-readable notes, not sent to AI
	Installation string   `json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
}

// ListMCPServersRequest contains parameters for listing MCP servers.
type ListMCPServersRequest struct {
	Query     string `json:"query"`
	SortBy    string `json:"sort_by"`    // name, command
	SortOrder string `json:"sort_order"` // asc, desc

	// TestConfig is an optional pre-loaded config (for testing).
	// When set, skips config.Load() and uses this config instead.
	TestConfig *config.Config `json:"-"`
}

// ListMCPServersResult contains the list of MCP servers.
type ListMCPServersResult struct {
	Servers      []MCPServerEntry `json:"servers"`
	Count        int              `json:"count"`
	AutoRegister bool             `json:"auto_register"`
}

// ListMCPServers returns all configured MCP servers.
func ListMCPServers(ctx context.Context, cfg *config.Config, req ListMCPServersRequest) (*ListMCPServersResult, error) {
	// Use injected config for testing, otherwise reload for freshness.
	freshCfg, err := resolveListConfig(req.TestConfig)
	if err != nil {
		return nil, err
	}

	servers := collectMCPServers(freshCfg, strings.ToLower(req.Query))
	sortMCPServers(servers, req.SortBy, req.SortOrder)

	return &ListMCPServersResult{
		Servers:      servers,
		Count:        len(servers),
		AutoRegister: freshCfg.MCP.ShouldAutoRegisterCtxloom(),
	}, nil
}

// resolveListConfig returns the injected test config, or loads a fresh one.
func resolveListConfig(testConfig *config.Config) (*config.Config, error) {
	if testConfig != nil {
		return testConfig, nil
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
		Backend:      backend,
		Notes:        srv.Notes,
		Installation: srv.Installation,
	}
}

// collectMCPServers gathers unified and backend-specific servers matching query.
func collectMCPServers(cfg *config.Config, query string) []MCPServerEntry {
	var servers []MCPServerEntry
	for name, srv := range cfg.MCP.Servers {
		if mcpServerMatches(query, name, srv.Command) {
			servers = append(servers, mcpEntry(name, srv, "unified"))
		}
	}
	for backend, backendServers := range cfg.MCP.Plugins {
		for name, srv := range backendServers {
			if mcpServerMatches(query, name, srv.Command) {
				servers = append(servers, mcpEntry(name, srv, backend))
			}
		}
	}
	return servers
}

// sortMCPServers sorts servers in place by name or command. An empty sortBy
// defaults to name; any other value leaves the order untouched.
func sortMCPServers(servers []MCPServerEntry, sortBy, sortOrder string) {
	if sortBy == "" {
		sortBy = "name"
	}
	reverse := sortOrder == "desc"
	less := func(a, b string) bool {
		cmp := strings.Compare(strings.ToLower(a), strings.ToLower(b))
		if reverse {
			return cmp > 0
		}
		return cmp < 0
	}
	switch sortBy {
	case "name":
		sort.Slice(servers, func(i, j int) bool { return less(servers[i].Name, servers[j].Name) })
	case "command":
		sort.Slice(servers, func(i, j int) bool { return less(servers[i].Command, servers[j].Command) })
	}
}

// AddMCPServerRequest contains parameters for adding an MCP server.
type AddMCPServerRequest struct {
	Name         string   `json:"name"`
	Command      string   `json:"command"`
	Args         []string `json:"args"`
	Backend      string   `json:"backend"`      // unified, claude-code, gemini
	Notes        string   `json:"notes"`        // Human-readable notes, not sent to AI
	Installation string   `json:"installation"` // Setup/installation instructions, not sent to AI

	// TestConfig is an optional pre-loaded config (for testing).
	// When set, skips config.Load() and Save(), returning modified config in result.
	TestConfig *config.Config `json:"-"`

	// FS is an optional filesystem for testing. Used with AppDir.
	FS afero.Fs `json:"-"`
	// AppDir is the ctxloom directory path. Required when FS is set.
	AppDir string `json:"-"`
}

// AddMCPServerResult contains the result of adding an MCP server.
type AddMCPServerResult struct {
	Status  string         `json:"status"`
	Name    string         `json:"name"`
	Command string         `json:"command"`
	Backend string         `json:"backend"`
	Message string         `json:"message"` // Operational status message
	Config  *config.Config `json:"-"`       // Updated config for caller to store
}

// AddMCPServer adds a new MCP server configuration.
func AddMCPServer(ctx context.Context, cfg *config.Config, req AddMCPServerRequest) (*AddMCPServerResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	freshCfg, err := loadFreshConfig(req.FS, req.AppDir, req.TestConfig)
	if err != nil {
		return nil, err
	}

	server := wire.MCPServer{
		Command:      req.Command,
		Args:         req.Args,
		Notes:        req.Notes,
		Installation: req.Installation,
	}

	if isUnifiedBackend(req.Backend) {
		err = addUnifiedServer(freshCfg, req.Name, server)
	} else {
		err = addBackendServer(freshCfg, req.Backend, req.Name, server)
	}
	if err != nil {
		return nil, err
	}

	if err := saveMCPConfig(freshCfg, req.TestConfig); err != nil {
		return nil, err
	}

	scope := "unified"
	if !isUnifiedBackend(req.Backend) {
		scope = req.Backend
	}

	return &AddMCPServerResult{
		Status:  "added",
		Name:    req.Name,
		Command: req.Command,
		Backend: scope,
		Message: "Run apply_hooks to inject into backend settings",
		Config:  freshCfg,
	}, nil
}

// isUnifiedBackend reports whether a backend value targets the unified server
// map (empty or the explicit "unified" label).
func isUnifiedBackend(backend string) bool {
	return backend == "" || backend == "unified"
}

// addUnifiedServer inserts server into the unified map, erroring if name exists.
func addUnifiedServer(cfg *config.Config, name string, server wire.MCPServer) error {
	if cfg.MCP.Servers == nil {
		cfg.MCP.Servers = make(map[string]wire.MCPServer)
	}
	if _, exists := cfg.MCP.Servers[name]; exists {
		return fmt.Errorf("MCP server %q already exists", name)
	}
	cfg.MCP.Servers[name] = server
	return nil
}

// addBackendServer inserts server into a backend's plugin map, erroring if name
// already exists for that backend.
func addBackendServer(cfg *config.Config, backend, name string, server wire.MCPServer) error {
	if cfg.MCP.Plugins == nil {
		cfg.MCP.Plugins = make(map[string]map[string]wire.MCPServer)
	}
	if cfg.MCP.Plugins[backend] == nil {
		cfg.MCP.Plugins[backend] = make(map[string]wire.MCPServer)
	}
	if _, exists := cfg.MCP.Plugins[backend][name]; exists {
		return fmt.Errorf("MCP server %q already exists for backend %s", name, backend)
	}
	cfg.MCP.Plugins[backend][name] = server
	return nil
}

// saveMCPConfig persists cfg unless a test config was injected (in which case
// the caller inspects the returned config instead). With an afero FS but no
// test config, Save writes to that FS.
func saveMCPConfig(cfg *config.Config, testConfig *config.Config) error {
	if testConfig != nil {
		return nil
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	return nil
}

// RemoveMCPServerRequest contains parameters for removing an MCP server.
type RemoveMCPServerRequest struct {
	Name    string `json:"name"`
	Backend string `json:"backend"` // unified, claude-code, gemini, or empty for all

	// TestConfig is an optional pre-loaded config (for testing).
	// When set, skips config.Load() and Save(), returning modified config in result.
	TestConfig *config.Config `json:"-"`

	// FS is an optional filesystem for testing. Used with AppDir.
	FS afero.Fs `json:"-"`
	// AppDir is the ctxloom directory path. Required when FS is set.
	AppDir string `json:"-"`
}

// RemoveMCPServerResult contains the result of removing an MCP server.
type RemoveMCPServerResult struct {
	Status      string         `json:"status"`
	Name        string         `json:"name"`
	RemovedFrom []string       `json:"removed_from"`
	Message     string         `json:"message"` // Operational status message
	Config      *config.Config `json:"-"`       // Updated config for caller to store
}

// RemoveMCPServer removes an MCP server configuration.
func RemoveMCPServer(ctx context.Context, cfg *config.Config, req RemoveMCPServerRequest) (*RemoveMCPServerResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	freshCfg, err := loadFreshConfig(req.FS, req.AppDir, req.TestConfig)
	if err != nil {
		return nil, err
	}

	removedFrom := removeMCPServerEntries(freshCfg, req.Backend, req.Name)
	if len(removedFrom) == 0 {
		return nil, fmt.Errorf("MCP server %q not found", req.Name)
	}

	if err := saveMCPConfig(freshCfg, req.TestConfig); err != nil {
		return nil, err
	}

	return &RemoveMCPServerResult{
		Status:      "removed",
		Name:        req.Name,
		RemovedFrom: removedFrom,
		Message:     "Run apply_hooks to update backend settings",
		Config:      freshCfg,
	}, nil
}

// removeMCPServerEntries deletes the named server from the locations implied by
// backend, returning the list of locations it was removed from. An empty
// backend removes from the unified map and every plugin backend; "unified"
// removes only from the unified map; any other value removes from the unified
// map (always checked) and that specific backend.
func removeMCPServerEntries(cfg *config.Config, backend, name string) []string {
	var removedFrom []string
	if isUnifiedBackend(backend) && removeUnifiedServer(cfg, name) {
		removedFrom = append(removedFrom, "unified")
	}

	switch {
	case backend == "":
		removedFrom = append(removedFrom, removeFromAllBackends(cfg, name)...)
	case backend != "unified":
		if removeBackendServer(cfg, backend, name) {
			removedFrom = append(removedFrom, backend)
		}
	}
	return removedFrom
}

// removeUnifiedServer deletes name from the unified map, reporting whether it
// was present.
func removeUnifiedServer(cfg *config.Config, name string) bool {
	if _, exists := cfg.MCP.Servers[name]; !exists {
		return false
	}
	delete(cfg.MCP.Servers, name)
	return true
}

// removeBackendServer deletes name from a specific backend, reporting whether
// it was present.
func removeBackendServer(cfg *config.Config, backend, name string) bool {
	servers, ok := cfg.MCP.Plugins[backend]
	if !ok {
		return false
	}
	if _, exists := servers[name]; !exists {
		return false
	}
	delete(servers, name)
	return true
}

// removeFromAllBackends deletes name from every plugin backend, returning the
// backends it was removed from.
func removeFromAllBackends(cfg *config.Config, name string) []string {
	var removed []string
	for backend, servers := range cfg.MCP.Plugins {
		if _, exists := servers[name]; exists {
			delete(servers, name)
			removed = append(removed, backend)
		}
	}
	return removed
}

// SetMCPAutoRegisterRequest contains parameters for setting auto-register.
type SetMCPAutoRegisterRequest struct {
	Enabled bool `json:"enabled"`

	// TestConfig is an optional pre-loaded config (for testing).
	// When set, skips config.Load() and Save(), returning modified config in result.
	TestConfig *config.Config `json:"-"`

	// FS is an optional filesystem for testing. Used with AppDir.
	FS afero.Fs `json:"-"`
	// AppDir is the ctxloom directory path. Required when FS is set.
	AppDir string `json:"-"`
}

// SetMCPAutoRegisterResult contains the result of setting auto-register.
type SetMCPAutoRegisterResult struct {
	Status       string         `json:"status"`
	AutoRegister bool           `json:"auto_register"`
	Message      string         `json:"message"` // Operational status message
	Config       *config.Config `json:"-"`       // Updated config for caller to store
}

// SetMCPAutoRegister enables or disables auto-registration of ctxloom's MCP server.
func SetMCPAutoRegister(ctx context.Context, cfg *config.Config, req SetMCPAutoRegisterRequest) (*SetMCPAutoRegisterResult, error) {
	freshCfg, err := loadFreshConfig(req.FS, req.AppDir, req.TestConfig)
	if err != nil {
		return nil, err
	}

	freshCfg.MCP.AutoRegisterCtxloom = &req.Enabled

	if err := saveMCPConfig(freshCfg, req.TestConfig); err != nil {
		return nil, err
	}

	return &SetMCPAutoRegisterResult{
		Status:       "updated",
		AutoRegister: req.Enabled,
		Message:      "Run apply_hooks to update backend settings",
		Config:       freshCfg,
	}, nil
}
