// Package operations tests verify the MCP server management operations.
//
// MCP (Model Context Protocol) servers are external processes that extend AI
// capabilities with tools like filesystem access, search, and integrations.
// These tests verify CRUD operations on MCP server configuration.
//
// # Architecture Notes
//
// MCP servers can be configured at two levels:
//   - Unified: Servers shared across all LLM backends (stored in mcp.servers)
//   - Backend-specific: Servers only available to one backend (stored in mcp.plugins.<backend>)
//
// The backend parameter controls where servers are stored:
//   - "" or "unified": The unified servers map
//   - "claude-code", "codex": Backend-specific maps
//
// # Test Injection Patterns
//
// Read-only operations (List/Get) take a *config.Config directly (the
// redundant TestConfig request field was deleted — cfg IS the injection
// seam). Write operations (Add/Remove/SetMCPAutoRegister) take a
// *config.Manager instead:
// they perform a real Manager.Update transaction, so their tests build a
// Manager over an in-memory filesystem (mcpTestManager) and verify the
// result by reloading the config from that same filesystem — a stub that
// skipped the transaction would stop testing the locked read-modify-write
// these functions exist to perform.
//
// # NON-OBVIOUS Behavior
//
// Remove operations with empty backend (Backend="") remove the server from
// ALL locations - unified AND all backend-specific maps. This is intentional
// for cleanup but can be surprising if you only expected unified removal.
package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestMCPServerEntry_Fields verifies the MCPServerEntry struct stores all
// expected fields for representing an MCP server in list results.
func TestMCPServerEntry_Fields(t *testing.T) {
	entry := MCPServerEntry{
		Name:    "my-server",
		Command: "npx",
		Args:    []string{"@my/server", "--port", "3000"},
		Backend: "unified",
	}

	assert.Equal(t, "my-server", entry.Name)
	assert.Equal(t, "npx", entry.Command)
	assert.Equal(t, []string{"@my/server", "--port", "3000"}, entry.Args)
	assert.Equal(t, "unified", entry.Backend)
}

func TestListMCPServersRequest_Defaults(t *testing.T) {
	req := ListMCPServersRequest{}

	assert.Empty(t, req.Query)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
}

func TestListMCPServersResult_Fields(t *testing.T) {
	result := ListMCPServersResult{
		Servers: []MCPServerEntry{
			{Name: "server1", Command: "npx"},
			{Name: "server2", Command: "python"},
		},
		Count: 2,
	}

	assert.Len(t, result.Servers, 2)
	assert.Equal(t, 2, result.Count)
}

func TestAddMCPServerRequest_Fields(t *testing.T) {
	req := AddMCPServerRequest{
		Name:    "new-server",
		Command: "node",
		Args:    []string{"server.js"},
		Backend: "claude-code",
	}

	assert.Equal(t, "new-server", req.Name)
	assert.Equal(t, "node", req.Command)
	assert.Equal(t, []string{"server.js"}, req.Args)
	assert.Equal(t, "claude-code", req.Backend)
}

func TestAddMCPServerRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         AddMCPServerRequest
		shouldError bool
	}{
		{
			name: "valid request",
			req: AddMCPServerRequest{
				Name:    "valid",
				Command: "npx",
			},
			shouldError: false,
		},
		{
			name: "missing name",
			req: AddMCPServerRequest{
				Name:    "",
				Command: "npx",
			},
			shouldError: true,
		},
		{
			name: "missing command",
			req: AddMCPServerRequest{
				Name:    "test",
				Command: "",
			},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				assert.True(t, tt.req.Name == "" || tt.req.Command == "")
			} else {
				assert.NotEmpty(t, tt.req.Name)
				assert.NotEmpty(t, tt.req.Command)
			}
		})
	}
}

func TestAddMCPServerResult_Fields(t *testing.T) {
	result := AddMCPServerResult{
		Status:  "added",
		Name:    "my-server",
		Command: "npx",
		Backend: "unified",
		Message: "Server added successfully",
	}

	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "my-server", result.Name)
	assert.Equal(t, "npx", result.Command)
	assert.Equal(t, "unified", result.Backend)
}

func TestRemoveMCPServerRequest_Fields(t *testing.T) {
	req := RemoveMCPServerRequest{
		Name:    "server-to-remove",
		Backend: "unified",
	}

	assert.Equal(t, "server-to-remove", req.Name)
	assert.Equal(t, "unified", req.Backend)
}

func TestRemoveMCPServerRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         RemoveMCPServerRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         RemoveMCPServerRequest{Name: "test"},
			shouldError: false,
		},
		{
			name:        "empty name",
			req:         RemoveMCPServerRequest{Name: ""},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				assert.Empty(t, tt.req.Name)
			} else {
				assert.NotEmpty(t, tt.req.Name)
			}
		})
	}
}

func TestRemoveMCPServerResult_Fields(t *testing.T) {
	result := RemoveMCPServerResult{
		Status:      "removed",
		Name:        "removed-server",
		RemovedFrom: []string{"unified"},
		Message:     "Server removed",
	}

	assert.Equal(t, "removed", result.Status)
	assert.Equal(t, "removed-server", result.Name)
	assert.Equal(t, []string{"unified"}, result.RemovedFrom)
}

func TestSetMCPAutoRegisterRequest_Fields(t *testing.T) {
	req := SetMCPAutoRegisterRequest{
		Enabled: true,
	}

	assert.True(t, req.Enabled)
}

func TestSetMCPAutoRegisterResult_Fields(t *testing.T) {
	result := SetMCPAutoRegisterResult{
		Status:       "enabled",
		AutoRegister: true,
		Message:      "Auto-register enabled",
	}

	assert.Equal(t, "enabled", result.Status)
	assert.True(t, result.AutoRegister)
}

func TestMCPBackendValues(t *testing.T) {
	validBackends := []string{"unified", "claude-code", "antigravity", ""}

	for _, backend := range validBackends {
		req := AddMCPServerRequest{
			Name:    "test",
			Command: "npx",
			Backend: backend,
		}
		assert.NotNil(t, req)
	}
}

// ==========================================================================
// Integration tests with injected config
// ==========================================================================
//
// The following tests exercise the full operation logic by passing an
// in-memory *config.Config as the cfg parameter directly. This allows
// testing sorting, filtering, and CRUD operations without filesystem access.

// createTestMCPConfig creates a config with servers in both unified and
// backend-specific locations for testing queries and sorting.
func createTestMCPConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		MCP: wire.MCPConfig{
			Servers: map[string]wire.MCPServer{
				"filesystem": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
				},
				"github": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-github"},
				},
			},
			Plugins: map[string]map[string]wire.MCPServer{
				"claude-code": {
					"custom-server": {
						Command: "python",
						Args:    []string{"server.py"},
					},
				},
			},
		},
	})
}

func TestGetMCPServer_UnifiedScope(t *testing.T) {
	cfg := createTestMCPConfig()
	result, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: "github"})
	require.NoError(t, err)
	assert.True(t, result.Found)
	require.Len(t, result.Entries, 1)
	assert.Equal(t, "github", result.Entries[0].Name)
	assert.Equal(t, "unified", result.Entries[0].Backend)
	assert.Equal(t, "npx", result.Entries[0].Command)
}

func TestGetMCPServer_BackendScope(t *testing.T) {
	cfg := createTestMCPConfig()
	result, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: "custom-server"})
	require.NoError(t, err)
	assert.True(t, result.Found)
	require.Len(t, result.Entries, 1)
	assert.Equal(t, "claude-code", result.Entries[0].Backend)
}

func TestGetMCPServer_NotFound(t *testing.T) {
	cfg := createTestMCPConfig()
	result, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: "absent"})
	require.NoError(t, err)
	assert.False(t, result.Found)
	assert.Empty(t, result.Entries, "not-found yields an empty (non-nil) entry list")
}

func TestGetMCPServer_MultipleScopes(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		MCP: wire.MCPConfig{
			Servers: map[string]wire.MCPServer{
				"shared": {Command: "unified-cmd"},
			},
			Plugins: map[string]map[string]wire.MCPServer{
				"claude-code": {"shared": {Command: "backend-cmd"}},
			},
		},
	})
	result, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: "shared"})
	require.NoError(t, err)
	assert.True(t, result.Found)
	require.Len(t, result.Entries, 2, "configured in both the unified and a backend scope")
	// Sorted by backend scope: "claude-code" < "unified".
	assert.Equal(t, "claude-code", result.Entries[0].Backend)
	assert.Equal(t, "unified", result.Entries[1].Backend)
}

func TestListMCPServers_AllServers(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{})

	require.NoError(t, err)
	assert.Equal(t, 3, result.Count) // filesystem, github, custom-server
	assert.Len(t, result.Servers, 3)
}

func TestListMCPServers_WithQuery(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		Query: "github",
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "github", result.Servers[0].Name)
}

func TestListMCPServers_QueryByCommand(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		Query: "python",
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "custom-server", result.Servers[0].Name)
}

func TestListMCPServers_SortByName(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		SortBy:    "name",
		SortOrder: "asc",
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Servers), 2)

	// Verify sorted ascending
	for i := 1; i < len(result.Servers); i++ {
		assert.LessOrEqual(t, result.Servers[i-1].Name, result.Servers[i].Name)
	}
}

func TestListMCPServers_SortByCommand(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		SortBy:    "command",
		SortOrder: "asc",
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Servers), 2)

	// Verify sorted ascending by command
	for i := 1; i < len(result.Servers); i++ {
		assert.LessOrEqual(t, result.Servers[i-1].Command, result.Servers[i].Command)
	}
}

func TestListMCPServers_SortDescending(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		SortBy:    "name",
		SortOrder: "desc",
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Servers), 2)

	// Verify sorted descending
	for i := 1; i < len(result.Servers); i++ {
		assert.GreaterOrEqual(t, result.Servers[i-1].Name, result.Servers[i].Name)
	}
}

func TestListMCPServers_SortByCommandDescending(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		SortBy:    "command",
		SortOrder: "desc",
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Servers), 2)

	// Verify sorted descending by command
	for i := 1; i < len(result.Servers); i++ {
		assert.GreaterOrEqual(t, result.Servers[i-1].Command, result.Servers[i].Command)
	}
}

func TestListMCPServers_QueryBackendServerByName(t *testing.T) {
	cfg := createTestMCPConfig()

	// Query for backend-specific server by name
	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		Query: "custom",
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "custom-server", result.Servers[0].Name)
	assert.Equal(t, "claude-code", result.Servers[0].Backend)
}

// mcpTestManager builds a Manager over a fresh in-memory filesystem seeded
// with the given config.yaml body ("" = no file at all, an empty/degraded
// config), returning the Manager plus the fs so a test can seed further
// fixtures or read the written file back directly. The write operations
// (Add/Remove/SetMCPAutoRegister) perform a real Manager.Update transaction
// against whatever this Manager targets, so this is the seam that exercises
// that transaction hermetically instead of the real disk.
func mcpTestManager(t *testing.T, yamlBody string) (*config.Manager, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	if yamlBody != "" {
		require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(testBaseDir), []byte(yamlBody), 0644))
	}
	return config.NewManager(config.WithFS(fs), config.WithAppDir(testBaseDir)), fs
}

// testHomeAppDir is the fixed, isolated home appDir mcpTestManagerWithHome
// writes to and t.Setenv("HOME", ...) points at.
const testHomeAppDir = "/home/u/" + paths.AppDirName

// mcpTestManagerWithHome is mcpTestManager plus an isolated HOME layer.
// mcp.servers.*.args/env remain ScopeMachine (internal/config/layerscope) —
// arguments/credentials on THIS box — so a fixture whose test cares about one
// of THOSE fields surviving a Manager.Update reload (as opposed to
// mcp.servers.*.command, ScopeShared and legal at the project layer again —
// see policy_default.go's divergence note) still needs it declared in HOME,
// representing a personally-configured MCP server the merged view still
// includes.
func mcpTestManagerWithHome(t *testing.T, homeBody, projectBody string) (*config.Manager, afero.Fs) {
	t.Helper()
	t.Setenv("HOME", "/home/u")
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(testBaseDir, 0755))
	if projectBody != "" {
		require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(testBaseDir), []byte(projectBody), 0644))
	}
	require.NoError(t, fs.MkdirAll(testHomeAppDir, 0755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(testHomeAppDir), []byte(homeBody), 0644))
	return config.NewManager(config.WithFS(fs), config.WithAppDir(testBaseDir)), fs
}

// reloadMCPTestConfig re-reads the config a mcpTestManager transaction wrote,
// so a test can assert against the PERSISTED result rather than a mutated
// argument (Manager.Update never mutates any *Config a caller holds).
func reloadMCPTestConfig(t *testing.T, fs afero.Fs) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(testBaseDir))
	require.NoError(t, err)
	return cfg
}

func TestAddMCPServer_UnifiedBackend(t *testing.T) {
	mgr, fs := mcpTestManager(t, "version: 5\n")

	result, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "new-server",
		Command: "node",
		Args:    []string{"server.js", "--port", "3000"},
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "new-server", result.Name)
	assert.Equal(t, "unified", result.Backend)

	// Verify server was added to config. mcp.servers.*.command is ScopeShared
	// (internal/config/layerscope) so it survives a real, fully-layered
	// reload; this only asserts on Command, so reloadMCPTestConfig (not the
	// Raw/ParseConfig variant) is the right check here.
	cfg := reloadMCPTestConfig(t, fs)
	assert.Contains(t, cfg.GetMCPConfig().Servers, "new-server")
	assert.Equal(t, "node", cfg.GetMCPConfig().Servers["new-server"].Command)
}

func TestAddMCPServer_SpecificBackend(t *testing.T) {
	mgr, fs := mcpTestManager(t, "version: 5\n")

	result, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "claude-specific",
		Command: "python",
		Args:    []string{"claude_server.py"},
		Backend: "claude-code",
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "claude-code", result.Backend)

	// Verify server was added to correct backend
	cfg := reloadMCPTestConfig(t, fs)
	assert.Contains(t, cfg.GetMCPConfig().Plugins["claude-code"], "claude-specific")
}

// TestAddMCPServer_ValidationErrors verifies that required fields are enforced.
// Both name and command are required - the server won't function without them.
func TestAddMCPServer_ValidationErrors(t *testing.T) {
	mgr, _ := mcpTestManager(t, "version: 5\n")

	tests := []struct {
		name        string
		req         AddMCPServerRequest
		errContains string
	}{
		{
			name: "missing name",
			req: AddMCPServerRequest{
				Name:    "",
				Command: "npx",
			},
			errContains: "name is required",
		},
		{
			name: "missing command",
			req: AddMCPServerRequest{
				Name:    "test",
				Command: "",
			},
			errContains: "command is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AddMCPServer(context.Background(), mgr, tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// TestAddMCPServer_AlreadyExists verifies that duplicate server names are rejected.
// This prevents accidental overwriting of existing server configurations.
//
// NON-OBVIOUS: The duplicate check is per-location. A server named "foo" can
// exist in unified AND in claude-code backend simultaneously. The check only
// fails when adding to the same location where the name already exists.
func TestAddMCPServer_AlreadyExists(t *testing.T) {
	// mcp.servers.*.command is ScopeShared (internal/config/layerscope) — a
	// deliberate divergence from the design doc's literal ScopeMachine (see
	// policy_default.go's own comment): command is REQUIRED by the
	// mcpServer schema, so Machine-scoping it would make mcp.servers.* --
	// ScopeShared, "what the team wires in" -- impossible to populate with
	// a working entry from the project layer at all. A plain project-only
	// fixture is legal again.
	mgr, _ := mcpTestManager(t, "version: 5\nmcp:\n  servers:\n    existing:\n      command: npx\n")

	_, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "existing",
		Command: "node",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAddMCPServer_BackendAlreadyExists(t *testing.T) {
	mgr, _ := mcpTestManager(t, "version: 5\nmcp:\n  plugins:\n    claude-code:\n      existing:\n        command: npx\n")

	_, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "existing",
		Command: "node",
		Backend: "claude-code",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "claude-code")
}

// TestAddMCPServer_BackendNilMaps verifies that nil maps are initialized on demand.
// Users shouldn't need to pre-create empty maps in their config - the operation
// handles this automatically.
//
// This is part of ctxloom's fault-tolerant philosophy: work with whatever state
// the config is in, don't require perfect setup.
func TestAddMCPServer_BackendNilMaps(t *testing.T) {
	// Test that nil Plugins map is initialized
	mgr, fs := mcpTestManager(t, "version: 5\n")

	result, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "new-server",
		Command: "node",
		Backend: "antigravity",
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "antigravity", result.Backend)
	cfg := reloadMCPTestConfig(t, fs)
	assert.NotNil(t, cfg.GetMCPConfig().Plugins)
	assert.NotNil(t, cfg.GetMCPConfig().Plugins["antigravity"])
	assert.Contains(t, cfg.GetMCPConfig().Plugins["antigravity"], "new-server")
}

func TestRemoveMCPServer_FromUnified(t *testing.T) {
	// mcp.servers.*.command is ScopeShared (internal/config/layerscope) — a
	// deliberate divergence from the design doc (see policy_default.go's own
	// comment) — so a plain project-only fixture is legal again.
	mgr, fs := mcpTestManager(t, "version: 5\nmcp:\n  servers:\n    to-remove:\n      command: npx\n    keep:\n      command: node\n")

	result, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name: "to-remove",
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Contains(t, result.RemovedFrom, "unified")

	// Verify server was removed
	cfg := reloadMCPTestConfig(t, fs)
	assert.NotContains(t, cfg.GetMCPConfig().Servers, "to-remove")
	assert.Contains(t, cfg.GetMCPConfig().Servers, "keep")
}

func TestRemoveMCPServer_FromSpecificBackend(t *testing.T) {
	mgr, fs := mcpTestManager(t, "version: 5\nmcp:\n  plugins:\n    claude-code:\n      to-remove:\n        command: python\n      keep:\n        command: node\n")

	result, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name:    "to-remove",
		Backend: "claude-code",
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Contains(t, result.RemovedFrom, "claude-code")

	// Verify server was removed
	cfg := reloadMCPTestConfig(t, fs)
	assert.NotContains(t, cfg.GetMCPConfig().Plugins["claude-code"], "to-remove")
	assert.Contains(t, cfg.GetMCPConfig().Plugins["claude-code"], "keep")
}

func TestRemoveMCPServer_ValidationError(t *testing.T) {
	mgr, _ := mcpTestManager(t, "version: 5\n")

	_, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestRemoveMCPServer_NotFound(t *testing.T) {
	mgr, _ := mcpTestManager(t, "version: 5\n")

	_, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name: "nonexistent",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestRemoveMCPServer_FromAllBackends verifies the "remove from everywhere" behavior.
//
// NON-OBVIOUS: When Backend is empty (""), the remove operation searches
// ALL locations and removes the server from EVERY place it's found. This is
// the "nuclear option" for cleanup when a server name appears in multiple places.
//
// The RemovedFrom slice in the result tells you where it was actually removed from,
// which can be more than one location.
func TestRemoveMCPServer_FromAllBackends(t *testing.T) {
	// mcp.servers.*.command is ScopeShared (internal/config/layerscope) — a
	// deliberate divergence from the design doc (see policy_default.go's own
	// comment) — so both mcp.servers.* and mcp.plugins.* are legal in one
	// plain project-only fixture again.
	mgr, fs := mcpTestManager(t,
		"version: 5\n"+
			"mcp:\n"+
			"  servers:\n"+
			"    multi-server:\n"+
			"      command: unified-cmd\n"+
			"    keep:\n"+
			"      command: keep-cmd\n"+
			"  plugins:\n"+
			"    claude-code:\n"+
			"      multi-server:\n"+
			"        command: claude-cmd\n"+
			"      other:\n"+
			"        command: other-cmd\n"+
			"    antigravity:\n"+
			"      multi-server:\n"+
			"        command: antigravity-cmd\n")

	result, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name:    "multi-server",
		Backend: "", // Empty = remove from everywhere
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	// Should be removed from unified and all backends
	assert.GreaterOrEqual(t, len(result.RemovedFrom), 2)
	assert.Contains(t, result.RemovedFrom, "unified")

	// Verify removal against the fully-merged (layered) view: every field
	// touched here is ScopeShared, so a real reload preserves it.
	cfg := reloadMCPTestConfig(t, fs)
	_, existsInUnified := cfg.GetMCPConfig().Servers["multi-server"]
	assert.False(t, existsInUnified)
	_, existsInClaude := cfg.GetMCPConfig().Plugins["claude-code"]["multi-server"]
	assert.False(t, existsInClaude)
	_, existsInAntigravity := cfg.GetMCPConfig().Plugins["antigravity"]["multi-server"]
	assert.False(t, existsInAntigravity)

	// Other servers should remain
	assert.Contains(t, cfg.GetMCPConfig().Servers, "keep")
	assert.Contains(t, cfg.GetMCPConfig().Plugins["claude-code"], "other")
}

func TestSetMCPAutoRegister_Enable(t *testing.T) {
	mgr, fs := mcpTestManager(t, "version: 5\n")

	result, err := SetMCPAutoRegister(context.Background(), mgr, SetMCPAutoRegisterRequest{
		Enabled: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.True(t, result.AutoRegister)
	cfg := reloadMCPTestConfig(t, fs)
	assert.NotNil(t, cfg.GetMCPConfig().AutoRegisterCtxloom)
	assert.True(t, *cfg.GetMCPConfig().AutoRegisterCtxloom)
}

func TestSetMCPAutoRegister_Disable(t *testing.T) {
	mgr, fs := mcpTestManager(t, "version: 5\nmcp:\n  auto_register_ctxloom: true\n")

	result, err := SetMCPAutoRegister(context.Background(), mgr, SetMCPAutoRegisterRequest{
		Enabled: false,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.False(t, result.AutoRegister)
	cfg := reloadMCPTestConfig(t, fs)
	assert.NotNil(t, cfg.GetMCPConfig().AutoRegisterCtxloom)
	assert.False(t, *cfg.GetMCPConfig().AutoRegisterCtxloom)
}

func TestSetMCPAutoRegister_WithFS(t *testing.T) {
	mgr, fs := mcpTestManager(t, "llm:\n  plugins: {}\n")

	result, err := SetMCPAutoRegister(context.Background(), mgr, SetMCPAutoRegisterRequest{
		Enabled: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.True(t, result.AutoRegister)

	// Verify the config was saved to the filesystem
	data, err := afero.ReadFile(fs, paths.ConfigPath(testBaseDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_register_ctxloom: true")
}

// ==========================================================================
// Filesystem-based integration tests
// ==========================================================================
//
// The following tests inspect the RAW bytes Manager.Update wrote to a virtual
// filesystem, complementing the accessor-level checks above.

// TestAddMCPServer_WithFS verifies the complete add flow including config save.
func TestAddMCPServer_WithFS(t *testing.T) {
	mgr, fs := mcpTestManager(t, "llm:\n  plugins: {}\n")

	result, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "test-server",
		Command: "npx",
		Args:    []string{"@test/server"},
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "test-server", result.Name)
	assert.Equal(t, "npx", result.Command)

	// Verify the config was saved to the filesystem
	data, err := afero.ReadFile(fs, paths.ConfigPath(testBaseDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "test-server")
	assert.Contains(t, string(data), "npx")
}

// TestAddMCPServer_WithFS_InvalidYAML pins the CORRECTED contract.
//
// This test previously asserted the opposite, describing it as "ctxloom's
// fault-tolerant behavior: a pre-existing config.yaml with invalid YAML never
// blocks a write". What actually happened is that the write REPLACED the
// user's unparseable file with a fresh one built from the sections the
// in-memory Config could emit — every unmodelled key, and every modelled key
// this Config did not happen to carry, silently destroyed by a command the
// user ran to add an MCP server. "Resilience" that overwrites the input is
// data loss with a friendly name.
//
// The write is now refused and the file is left exactly as the user wrote it,
// so the one broken line can still be fixed.
func TestAddMCPServer_WithFS_InvalidYAML(t *testing.T) {
	const corrupt = "{{invalid yaml"
	mgr, fs := mcpTestManager(t, corrupt)

	_, err := AddMCPServer(context.Background(), mgr, AddMCPServerRequest{
		Name:    "test-server",
		Command: "npx",
	})
	require.Error(t, err, "a config.yaml that does not parse must not be overwritten")

	data, rerr := afero.ReadFile(fs, paths.ConfigPath(testBaseDir))
	require.NoError(t, rerr)
	assert.Equal(t, corrupt, string(data), "the user's file must be left untouched")
}

func TestRemoveMCPServer_WithFS(t *testing.T) {
	// existing-server's command lives in the PROJECT fixture
	// (mcp.servers.*.command is ScopeShared — internal/config/layerscope,
	// legal at the project layer) and its env lives in HOME
	// (mcp.servers.*.env remains ScopeMachine — credentials on this box, the
	// one sibling field NOT reclassified — see policy_default.go's
	// divergence comments). This split is what lets the test exercise a
	// genuinely cross-layer case: a project-scoped Manager has no mechanism
	// to durably delete a FIELD contributed by a LOWER layer (deep-merge
	// inheritance has no delete-marker, a pre-existing layering property
	// this closure did not introduce) — home's env survives the
	// project-scoped removal below even though the operation as a whole
	// correctly reports success.
	mgr, fs := mcpTestManagerWithHome(t,
		"mcp:\n  servers:\n    existing-server:\n      env:\n        API_KEY: secret\n",
		"llm:\n  plugins: {}\n"+
			"mcp:\n  servers:\n    existing-server:\n      command: npx\n")

	result, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name: "existing-server",
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Equal(t, "existing-server", result.Name)

	// RemoveMCPServer's WRITE only ever targets the PROJECT layer (Manager
	// was constructed WithAppDir(testBaseDir)) — its own command entry is
	// gone, proving the removal actually landed there.
	projectData, err := afero.ReadFile(fs, paths.ConfigPath(testBaseDir))
	require.NoError(t, err)
	assert.NotContains(t, string(projectData), "existing-server")

	// HOME's own env contribution is untouched: there is no mechanism for a
	// project-scoped Manager to durably delete a field declared in a LOWER
	// layer (see this test's own opening comment).
	homeData, err := afero.ReadFile(fs, paths.ConfigPath(testHomeAppDir))
	require.NoError(t, err)
	assert.Contains(t, string(homeData), "existing-server",
		"home's own copy is untouched -- Manager cannot write there")
}

func TestRemoveMCPServer_WithFS_InvalidYAML(t *testing.T) {
	mgr, _ := mcpTestManager(t, "{{invalid yaml")

	_, err := RemoveMCPServer(context.Background(), mgr, RemoveMCPServerRequest{
		Name: "test-server",
	})

	// With resilient startup, config loads successfully but server won't exist
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
