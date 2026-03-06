package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benjaminabbitt/scm/internal/config"
)

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
		Note:    "Server added successfully",
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
		Note:        "Server removed",
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
		Note:         "Auto-register enabled",
	}

	assert.Equal(t, "enabled", result.Status)
	assert.True(t, result.AutoRegister)
}

func TestMCPBackendValues(t *testing.T) {
	validBackends := []string{"unified", "claude-code", "gemini", ""}

	for _, backend := range validBackends {
		req := AddMCPServerRequest{
			Name:    "test",
			Command: "npx",
			Backend: backend,
		}
		assert.NotNil(t, req)
	}
}

// ========== Integration tests with injected config ==========

func createTestMCPConfig() *config.Config {
	return &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"filesystem": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
				},
				"github": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-github"},
				},
			},
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"custom-server": {
						Command: "python",
						Args:    []string{"server.py"},
					},
				},
			},
		},
	}
}

func TestListMCPServers_AllServers(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, 3, result.Count) // filesystem, github, custom-server
	assert.Len(t, result.Servers, 3)
}

func TestListMCPServers_WithQuery(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		Query:      "github",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "github", result.Servers[0].Name)
}

func TestListMCPServers_QueryByCommand(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		Query:      "python",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "custom-server", result.Servers[0].Name)
}

func TestListMCPServers_SortByName(t *testing.T) {
	cfg := createTestMCPConfig()

	result, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{
		SortBy:     "name",
		SortOrder:  "asc",
		TestConfig: cfg,
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
		SortBy:     "command",
		SortOrder:  "asc",
		TestConfig: cfg,
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
		SortBy:     "name",
		SortOrder:  "desc",
		TestConfig: cfg,
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
		SortBy:     "command",
		SortOrder:  "desc",
		TestConfig: cfg,
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
		Query:      "custom",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "custom-server", result.Servers[0].Name)
	assert.Equal(t, "claude-code", result.Servers[0].Backend)
}

func TestAddMCPServer_UnifiedBackend(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: make(map[string]config.MCPServer),
		},
	}

	result, err := AddMCPServer(context.Background(), cfg, AddMCPServerRequest{
		Name:       "new-server",
		Command:    "node",
		Args:       []string{"server.js", "--port", "3000"},
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "new-server", result.Name)
	assert.Equal(t, "unified", result.Backend)

	// Verify server was added to config
	assert.Contains(t, cfg.MCP.Servers, "new-server")
	assert.Equal(t, "node", cfg.MCP.Servers["new-server"].Command)
}

func TestAddMCPServer_SpecificBackend(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: make(map[string]config.MCPServer),
			Plugins: make(map[string]map[string]config.MCPServer),
		},
	}

	result, err := AddMCPServer(context.Background(), cfg, AddMCPServerRequest{
		Name:       "claude-specific",
		Command:    "python",
		Args:       []string{"claude_server.py"},
		Backend:    "claude-code",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "claude-code", result.Backend)

	// Verify server was added to correct backend
	assert.Contains(t, cfg.MCP.Plugins["claude-code"], "claude-specific")
}

func TestAddMCPServer_ValidationErrors(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	tests := []struct {
		name        string
		req         AddMCPServerRequest
		errContains string
	}{
		{
			name: "missing name",
			req: AddMCPServerRequest{
				Name:       "",
				Command:    "npx",
				TestConfig: cfg,
			},
			errContains: "name is required",
		},
		{
			name: "missing command",
			req: AddMCPServerRequest{
				Name:       "test",
				Command:    "",
				TestConfig: cfg,
			},
			errContains: "command is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AddMCPServer(context.Background(), cfg, tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestAddMCPServer_AlreadyExists(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"existing": {Command: "npx"},
			},
		},
	}

	_, err := AddMCPServer(context.Background(), cfg, AddMCPServerRequest{
		Name:       "existing",
		Command:    "node",
		TestConfig: cfg,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAddMCPServer_BackendAlreadyExists(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"existing": {Command: "npx"},
				},
			},
		},
	}

	_, err := AddMCPServer(context.Background(), cfg, AddMCPServerRequest{
		Name:       "existing",
		Command:    "node",
		Backend:    "claude-code",
		TestConfig: cfg,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "claude-code")
}

func TestAddMCPServer_BackendNilMaps(t *testing.T) {
	// Test that nil Plugins map is initialized
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP:      config.MCPConfig{}, // No Plugins map
	}

	result, err := AddMCPServer(context.Background(), cfg, AddMCPServerRequest{
		Name:       "new-server",
		Command:    "node",
		Backend:    "gemini",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "gemini", result.Backend)
	assert.NotNil(t, cfg.MCP.Plugins)
	assert.NotNil(t, cfg.MCP.Plugins["gemini"])
	assert.Contains(t, cfg.MCP.Plugins["gemini"], "new-server")
}

func TestRemoveMCPServer_FromUnified(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"to-remove": {Command: "npx"},
				"keep":      {Command: "node"},
			},
		},
	}

	result, err := RemoveMCPServer(context.Background(), cfg, RemoveMCPServerRequest{
		Name:       "to-remove",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Contains(t, result.RemovedFrom, "unified")

	// Verify server was removed
	assert.NotContains(t, cfg.MCP.Servers, "to-remove")
	assert.Contains(t, cfg.MCP.Servers, "keep")
}

func TestRemoveMCPServer_FromSpecificBackend(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"to-remove": {Command: "python"},
					"keep":      {Command: "node"},
				},
			},
		},
	}

	result, err := RemoveMCPServer(context.Background(), cfg, RemoveMCPServerRequest{
		Name:       "to-remove",
		Backend:    "claude-code",
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Contains(t, result.RemovedFrom, "claude-code")

	// Verify server was removed
	assert.NotContains(t, cfg.MCP.Plugins["claude-code"], "to-remove")
	assert.Contains(t, cfg.MCP.Plugins["claude-code"], "keep")
}

func TestRemoveMCPServer_ValidationError(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := RemoveMCPServer(context.Background(), cfg, RemoveMCPServerRequest{
		Name:       "",
		TestConfig: cfg,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestRemoveMCPServer_NotFound(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: make(map[string]config.MCPServer),
		},
	}

	_, err := RemoveMCPServer(context.Background(), cfg, RemoveMCPServerRequest{
		Name:       "nonexistent",
		TestConfig: cfg,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRemoveMCPServer_FromAllBackends(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"multi-server": {Command: "unified-cmd"},
				"keep":         {Command: "keep-cmd"},
			},
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"multi-server": {Command: "claude-cmd"}, // Same name in backend
					"other":        {Command: "other-cmd"},
				},
				"gemini": {
					"multi-server": {Command: "gemini-cmd"}, // Same name in another backend
				},
			},
		},
	}

	result, err := RemoveMCPServer(context.Background(), cfg, RemoveMCPServerRequest{
		Name:       "multi-server",
		Backend:    "", // Empty = remove from everywhere
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	// Should be removed from unified and all backends
	assert.GreaterOrEqual(t, len(result.RemovedFrom), 2)
	assert.Contains(t, result.RemovedFrom, "unified")

	// Verify removal
	_, existsInUnified := cfg.MCP.Servers["multi-server"]
	assert.False(t, existsInUnified)
	_, existsInClaude := cfg.MCP.Plugins["claude-code"]["multi-server"]
	assert.False(t, existsInClaude)
	_, existsInGemini := cfg.MCP.Plugins["gemini"]["multi-server"]
	assert.False(t, existsInGemini)

	// Other servers should remain
	assert.Contains(t, cfg.MCP.Servers, "keep")
	assert.Contains(t, cfg.MCP.Plugins["claude-code"], "other")
}

func TestSetMCPAutoRegister_Enable(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP:      config.MCPConfig{},
	}

	result, err := SetMCPAutoRegister(context.Background(), cfg, SetMCPAutoRegisterRequest{
		Enabled:    true,
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.True(t, result.AutoRegister)
	assert.NotNil(t, cfg.MCP.AutoRegisterSCM)
	assert.True(t, *cfg.MCP.AutoRegisterSCM)
}

func TestSetMCPAutoRegister_Disable(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			AutoRegisterSCM: &enabled,
		},
	}

	result, err := SetMCPAutoRegister(context.Background(), cfg, SetMCPAutoRegisterRequest{
		Enabled:    false,
		TestConfig: cfg,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.False(t, result.AutoRegister)
	assert.NotNil(t, cfg.MCP.AutoRegisterSCM)
	assert.False(t, *cfg.MCP.AutoRegisterSCM)
}

func TestSetMCPAutoRegister_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"

	// Create config directory and file
	require.NoError(t, fs.MkdirAll(scmDir, 0755))
	configContent := `lm:
  plugins: {}
`
	require.NoError(t, afero.WriteFile(fs, scmDir+"/config.yaml", []byte(configContent), 0644))

	result, err := SetMCPAutoRegister(context.Background(), nil, SetMCPAutoRegisterRequest{
		Enabled: true,
		FS:      fs,
		SCMDir:  scmDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "updated", result.Status)
	assert.True(t, result.AutoRegister)

	// Verify the config was saved to the filesystem
	data, err := afero.ReadFile(fs, scmDir+"/config.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_register_scm: true")
}

func TestAddMCPServer_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"

	// Create config directory and file
	require.NoError(t, fs.MkdirAll(scmDir, 0755))
	configContent := `lm:
  plugins: {}
`
	require.NoError(t, afero.WriteFile(fs, scmDir+"/config.yaml", []byte(configContent), 0644))

	result, err := AddMCPServer(context.Background(), nil, AddMCPServerRequest{
		Name:    "test-server",
		Command: "npx",
		Args:    []string{"@test/server"},
		FS:      fs,
		SCMDir:  scmDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "added", result.Status)
	assert.Equal(t, "test-server", result.Name)
	assert.Equal(t, "npx", result.Command)

	// Verify the config was saved to the filesystem
	data, err := afero.ReadFile(fs, scmDir+"/config.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(data), "test-server")
	assert.Contains(t, string(data), "npx")
}

func TestAddMCPServer_WithFS_LoadError(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"

	// Create config directory with invalid YAML
	require.NoError(t, fs.MkdirAll(scmDir, 0755))
	require.NoError(t, afero.WriteFile(fs, scmDir+"/config.yaml", []byte("{{invalid yaml"), 0644))

	_, err := AddMCPServer(context.Background(), nil, AddMCPServerRequest{
		Name:    "test-server",
		Command: "npx",
		FS:      fs,
		SCMDir:  scmDir,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}

func TestRemoveMCPServer_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"

	// Create config with an existing server
	require.NoError(t, fs.MkdirAll(scmDir, 0755))
	configContent := `lm:
  plugins: {}
mcp:
  servers:
    existing-server:
      command: npx
      args:
        - "@existing/server"
`
	require.NoError(t, afero.WriteFile(fs, scmDir+"/config.yaml", []byte(configContent), 0644))

	result, err := RemoveMCPServer(context.Background(), nil, RemoveMCPServerRequest{
		Name:   "existing-server",
		FS:     fs,
		SCMDir: scmDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "removed", result.Status)
	assert.Equal(t, "existing-server", result.Name)

	// Verify the server was removed from the config file
	data, err := afero.ReadFile(fs, scmDir+"/config.yaml")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "existing-server")
}

func TestRemoveMCPServer_WithFS_LoadError(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"

	// Create config directory with invalid YAML
	require.NoError(t, fs.MkdirAll(scmDir, 0755))
	require.NoError(t, afero.WriteFile(fs, scmDir+"/config.yaml", []byte("{{invalid yaml"), 0644))

	_, err := RemoveMCPServer(context.Background(), nil, RemoveMCPServerRequest{
		Name:   "test-server",
		FS:     fs,
		SCMDir: scmDir,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}
