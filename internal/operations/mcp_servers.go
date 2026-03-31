package operations

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
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
	// Use injected config for testing, otherwise reload for freshness
	freshCfg := req.TestConfig
	if freshCfg == nil {
		var err error
		freshCfg, err = config.Load()
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	var servers []MCPServerEntry
	query := strings.ToLower(req.Query)

	// Unified servers
	for name, srv := range freshCfg.MCP.Servers {
		if query != "" && !strings.Contains(strings.ToLower(name), query) &&
			!strings.Contains(strings.ToLower(srv.Command), query) {
			continue
		}
		servers = append(servers, MCPServerEntry{
			Name:         name,
			Command:      srv.Command,
			Args:         srv.Args,
			Backend:      "unified",
			Notes:        srv.Notes,
			Installation: srv.Installation,
		})
	}

	// Backend-specific servers
	for backend, backendServers := range freshCfg.MCP.Plugins {
		for name, srv := range backendServers {
			if query != "" && !strings.Contains(strings.ToLower(name), query) &&
				!strings.Contains(strings.ToLower(srv.Command), query) {
				continue
			}
			servers = append(servers, MCPServerEntry{
				Name:         name,
				Command:      srv.Command,
				Args:         srv.Args,
				Backend:      backend,
				Notes:        srv.Notes,
				Installation: srv.Installation,
			})
		}
	}

	// Sort results
	sortBy := req.SortBy
	if sortBy == "" {
		sortBy = "name"
	}
	reverse := req.SortOrder == "desc"

	switch sortBy {
	case "name":
		sort.Slice(servers, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(servers[i].Name), strings.ToLower(servers[j].Name))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	case "command":
		sort.Slice(servers, func(i, j int) bool {
			cmp := strings.Compare(strings.ToLower(servers[i].Command), strings.ToLower(servers[j].Command))
			if reverse {
				return cmp > 0
			}
			return cmp < 0
		})
	}

	return &ListMCPServersResult{
		Servers:      servers,
		Count:        len(servers),
		AutoRegister: freshCfg.MCP.ShouldAutoRegisterCtxloom(),
	}, nil
}

// AddMCPServerRequest contains parameters for adding an MCP server.
type AddMCPServerRequest struct {
	Name         string   `json:"name"`
	Command      string   `json:"command"`
	Args         []string `json:"args"`
	Backend      string   `json:"backend"` // unified, claude-code, gemini
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

	// Default filesystem
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	// Use injected config for testing, otherwise reload for freshness
	freshCfg := req.TestConfig
	if freshCfg == nil {
		opts := []config.LoadOption{config.WithFS(fs)}
		if req.AppDir != "" {
			opts = append(opts, config.WithAppDir(req.AppDir))
		}
		var err error
		freshCfg, err = config.Load(opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	server := config.MCPServer{
		Command:      req.Command,
		Args:         req.Args,
		Notes:        req.Notes,
		Installation: req.Installation,
	}

	if req.Backend == "" || req.Backend == "unified" {
		if freshCfg.MCP.Servers == nil {
			freshCfg.MCP.Servers = make(map[string]config.MCPServer)
		}
		if _, exists := freshCfg.MCP.Servers[req.Name]; exists {
			return nil, fmt.Errorf("MCP server %q already exists", req.Name)
		}
		freshCfg.MCP.Servers[req.Name] = server
	} else {
		if freshCfg.MCP.Plugins == nil {
			freshCfg.MCP.Plugins = make(map[string]map[string]config.MCPServer)
		}
		if freshCfg.MCP.Plugins[req.Backend] == nil {
			freshCfg.MCP.Plugins[req.Backend] = make(map[string]config.MCPServer)
		}
		if _, exists := freshCfg.MCP.Plugins[req.Backend][req.Name]; exists {
			return nil, fmt.Errorf("MCP server %q already exists for backend %s", req.Name, req.Backend)
		}
		freshCfg.MCP.Plugins[req.Backend][req.Name] = server
	}

	// Skip save when using test config
	if req.TestConfig == nil {
		if err := freshCfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	scope := "unified"
	if req.Backend != "" && req.Backend != "unified" {
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

	// Default filesystem
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	// Use injected config for testing, otherwise reload for freshness
	freshCfg := req.TestConfig
	if freshCfg == nil {
		opts := []config.LoadOption{config.WithFS(fs)}
		if req.AppDir != "" {
			opts = append(opts, config.WithAppDir(req.AppDir))
		}
		var err error
		freshCfg, err = config.Load(opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	removed := false
	removedFrom := []string{}

	// Remove from unified if no specific backend or if unified specified
	if req.Backend == "" || req.Backend == "unified" {
		if _, exists := freshCfg.MCP.Servers[req.Name]; exists {
			delete(freshCfg.MCP.Servers, req.Name)
			removed = true
			removedFrom = append(removedFrom, "unified")
		}
	}

	// Remove from backend-specific
	if req.Backend != "" && req.Backend != "unified" {
		if backendServers, ok := freshCfg.MCP.Plugins[req.Backend]; ok {
			if _, exists := backendServers[req.Name]; exists {
				delete(backendServers, req.Name)
				removed = true
				removedFrom = append(removedFrom, req.Backend)
			}
		}
	} else if req.Backend == "" {
		// If no backend specified, try to remove from all backends
		for backend, servers := range freshCfg.MCP.Plugins {
			if _, exists := servers[req.Name]; exists {
				delete(servers, req.Name)
				removed = true
				removedFrom = append(removedFrom, backend)
			}
		}
	}

	if !removed {
		return nil, fmt.Errorf("MCP server %q not found", req.Name)
	}

	// Skip save when using test config
	if req.TestConfig == nil {
		if err := freshCfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return &RemoveMCPServerResult{
		Status:      "removed",
		Name:        req.Name,
		RemovedFrom: removedFrom,
		Message:     "Run apply_hooks to update backend settings",
		Config:      freshCfg,
	}, nil
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
	// Default filesystem
	fs := req.FS
	if fs == nil {
		fs = afero.NewOsFs()
	}

	// Use injected config for testing, otherwise reload for freshness
	freshCfg := req.TestConfig
	if freshCfg == nil {
		opts := []config.LoadOption{config.WithFS(fs)}
		if req.AppDir != "" {
			opts = append(opts, config.WithAppDir(req.AppDir))
		}
		var err error
		freshCfg, err = config.Load(opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
	}

	freshCfg.MCP.AutoRegisterCtxloom = &req.Enabled

	// Skip save when using test config (but not when using FS)
	if req.TestConfig == nil {
		if err := freshCfg.Save(); err != nil {
			return nil, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return &SetMCPAutoRegisterResult{
		Status:       "updated",
		AutoRegister: req.Enabled,
		Message:      "Run apply_hooks to update backend settings",
		Config:       freshCfg,
	}, nil
}
