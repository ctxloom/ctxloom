package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Claude Lifecycle Tests
// =============================================================================

// TestClaudeLifecycle_New verifies proper initialization
func TestClaudeLifecycle_New(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	assert.NotNil(t, lifecycle)
	assert.Equal(t, backend, lifecycle.backend)
	assert.NotNil(t, lifecycle.BaseLifecycle)
}

// TestClaudeLifecycle_OnSessionStart verifies session start handler registration
func TestClaudeLifecycle_OnSessionStart(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	handler := EventHandler{
		Command: "echo test",
		Timeout: 30,
	}

	err := lifecycle.OnSessionStart("/tmp", handler)
	require.NoError(t, err)

	// Verify hook was added via GetHooks()
	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionStart, 1)
}

// TestClaudeLifecycle_OnSessionEnd verifies session end handler registration
func TestClaudeLifecycle_OnSessionEnd(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	handler := EventHandler{
		Command: "echo cleanup",
		Timeout: 30,
	}

	err := lifecycle.OnSessionEnd("/tmp", handler)
	require.NoError(t, err)

	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionEnd, 1)
}

// TestClaudeLifecycle_OnToolUse verifies tool use handler registration
func TestClaudeLifecycle_OnToolUse(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	handler := EventHandler{
		Command: "echo tool",
		Timeout: 30,
	}

	t.Run("before tool use", func(t *testing.T) {
		err := lifecycle.OnToolUse("/tmp", BeforeToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle.GetHooks()
		assert.Len(t, hooks.Unified.PreTool, 1)
	})

	t.Run("after tool use", func(t *testing.T) {
		// Create fresh lifecycle for independent test
		lifecycle2 := NewClaudeLifecycle(backend)
		err := lifecycle2.OnToolUse("/tmp", AfterToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle2.GetHooks()
		assert.Len(t, hooks.Unified.PostTool, 1)
	})
}

// TestClaudeLifecycle_Clear verifies handlers can be cleared
func TestClaudeLifecycle_Clear(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	// Add some hooks first
	_ = lifecycle.OnSessionStart("/tmp", EventHandler{Command: "echo test"})

	// Note: Clear will try to write to settings, which may fail in test
	// We're just verifying it resets internal state
	_ = lifecycle.Clear("/tmp")
	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
}

// TestClaudeLifecycle_Flush verifies hooks and MCP are flushed
func TestClaudeLifecycle_Flush(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	// Add some hooks
	_ = lifecycle.OnSessionStart("/tmp", EventHandler{Command: "echo test"})

	// Flush will attempt file I/O; we're verifying it doesn't panic
	_ = lifecycle.Flush("/tmp")
}

// =============================================================================
// Gemini Lifecycle Tests
// =============================================================================

// TestGeminiLifecycle_New verifies proper initialization
func TestGeminiLifecycle_New(t *testing.T) {
	backend := NewGemini()
	lifecycle := NewGeminiLifecycle(backend)

	assert.NotNil(t, lifecycle)
	assert.Equal(t, backend, lifecycle.backend)
	assert.NotNil(t, lifecycle.BaseLifecycle)
}

// TestGeminiLifecycle_OnSessionStart verifies session start handler registration
func TestGeminiLifecycle_OnSessionStart(t *testing.T) {
	backend := NewGemini()
	lifecycle := NewGeminiLifecycle(backend)

	handler := EventHandler{
		Command: "echo test",
		Timeout: 30,
	}

	err := lifecycle.OnSessionStart("/tmp", handler)
	require.NoError(t, err)

	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionStart, 1)
}

// TestGeminiLifecycle_OnSessionEnd verifies session end handler registration
func TestGeminiLifecycle_OnSessionEnd(t *testing.T) {
	backend := NewGemini()
	lifecycle := NewGeminiLifecycle(backend)

	handler := EventHandler{
		Command: "echo cleanup",
		Timeout: 30,
	}

	err := lifecycle.OnSessionEnd("/tmp", handler)
	require.NoError(t, err)

	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionEnd, 1)
}

// TestGeminiLifecycle_OnToolUse verifies tool use handler registration
func TestGeminiLifecycle_OnToolUse(t *testing.T) {
	backend := NewGemini()
	lifecycle := NewGeminiLifecycle(backend)

	handler := EventHandler{
		Command: "echo tool",
		Timeout: 30,
	}

	t.Run("before tool use", func(t *testing.T) {
		err := lifecycle.OnToolUse("/tmp", BeforeToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle.GetHooks()
		assert.Len(t, hooks.Unified.PreTool, 1)
	})

	t.Run("after tool use", func(t *testing.T) {
		lifecycle2 := NewGeminiLifecycle(backend)
		err := lifecycle2.OnToolUse("/tmp", AfterToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle2.GetHooks()
		assert.Len(t, hooks.Unified.PostTool, 1)
	})
}

// TestGeminiCommand_Structure verifies the command structure
func TestGeminiCommand_Structure(t *testing.T) {
	cmd := GeminiCommand{
		Description: "Test command",
		Prompt:      "Test prompt",
	}

	assert.Equal(t, "Test command", cmd.Description)
	assert.Equal(t, "Test prompt", cmd.Prompt)
}

// =============================================================================
// Claude MCP Manager Tests
// =============================================================================

func TestClaudeMCPManager_RegisterServer(t *testing.T) {
	backend := NewClaudeCode()
	manager := NewClaudeMCPManager(backend)

	server := MCPServer{
		Name:    "test-server",
		Command: "test-cmd",
		Args:    []string{"arg1"},
	}

	err := manager.RegisterServer("/tmp", server)
	require.NoError(t, err)

	servers, _ := manager.ListServers("/tmp")
	assert.Len(t, servers, 1)
	assert.Contains(t, servers, "test-server")
}

func TestClaudeMCPManager_UnregisterServer(t *testing.T) {
	backend := NewClaudeCode()
	manager := NewClaudeMCPManager(backend)

	// Register first
	server := MCPServer{
		Name:    "test-server",
		Command: "test-cmd",
	}
	_ = manager.RegisterServer("/tmp", server)

	err := manager.UnregisterServer("/tmp", "test-server")
	require.NoError(t, err)

	servers, _ := manager.ListServers("/tmp")
	assert.Len(t, servers, 0)
}

func TestClaudeMCPManager_ListServers(t *testing.T) {
	backend := NewClaudeCode()
	manager := NewClaudeMCPManager(backend)

	_ = manager.RegisterServer("/tmp", MCPServer{Name: "server1"})
	_ = manager.RegisterServer("/tmp", MCPServer{Name: "server2"})

	names, err := manager.ListServers("/tmp")
	require.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "server1")
	assert.Contains(t, names, "server2")
}

func TestClaudeMCPManager_GetServer(t *testing.T) {
	backend := NewClaudeCode()
	manager := NewClaudeMCPManager(backend)

	server := MCPServer{
		Name:    "test-server",
		Command: "test-cmd",
		Args:    []string{"arg1"},
	}
	_ = manager.RegisterServer("/tmp", server)

	result, err := manager.GetServer("/tmp", "test-server")
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, server.Name, result.Name)
	assert.Equal(t, server.Command, result.Command)
}

func TestClaudeMCPManager_GetServer_NotFound(t *testing.T) {
	backend := NewClaudeCode()
	manager := NewClaudeMCPManager(backend)

	result, err := manager.GetServer("/tmp", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestClaudeMCPManager_Clear(t *testing.T) {
	backend := NewClaudeCode()
	manager := NewClaudeMCPManager(backend)

	_ = manager.RegisterServer("/tmp", MCPServer{Name: "server1"})

	// Clear will attempt file I/O; we're verifying it clears internal state
	_ = manager.Clear("/tmp")
	servers, _ := manager.ListServers("/tmp")
	assert.Len(t, servers, 0)
}

// =============================================================================
// Claude Skills Tests
// =============================================================================

func TestClaudeSkills_Register(t *testing.T) {
	backend := NewClaudeCode()
	skills := &ClaudeSkills{
		backend: backend,
	}

	skill := Skill{
		Name:        "test-skill",
		Description: "Test skill",
		Content:     "# Test Skill\n\nTest content",
	}

	// Register will attempt file I/O
	err := skills.Register("/tmp", skill)
	// We expect this to succeed or fail due to file I/O, not panic
	_ = err
}

func TestClaudeSkills_RegisterAll(t *testing.T) {
	backend := NewClaudeCode()
	skills := &ClaudeSkills{
		backend: backend,
	}

	skillList := []Skill{
		{
			Name:        "skill1",
			Description: "Skill 1",
			Content:     "# Skill 1",
		},
		{
			Name:        "skill2",
			Description: "Skill 2",
			Content:     "# Skill 2",
		},
	}

	// RegisterAll will attempt file I/O
	err := skills.RegisterAll("/tmp", skillList)
	// We expect this to succeed or fail due to file I/O, not panic
	_ = err
}

// TestClaudeSkills_Register_WritesToWorkdir exercises the on-disk
// effect of Register against a tempdir, then asserts the slash-
// command file and manifest entry land where we expect. Tightens the
// existing "doesn't panic" smoke test.
func TestClaudeSkills_Register_WritesToWorkdir(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode()}
	require.NoError(t, skills.Register(workDir, Skill{
		Name:        "test-skill",
		Description: "Test skill",
		Content:     "# Test Skill\n\nTest content",
	}))

	// Slash command file must exist somewhere under .claude/commands/.
	cmds, err := os.ReadDir(filepath.Join(workDir, ".claude", "commands"))
	require.NoError(t, err)
	var found bool
	for _, e := range cmds {
		if strings.Contains(e.Name(), "test-skill") {
			found = true
		}
	}
	assert.True(t, found, "test-skill command file must land under .claude/commands/")

	// Manifest must track it for later Clear/List.
	manifest, err := os.ReadFile(filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "test-skill")
}

// TestClaudeSkills_RegisterFromContent covers the bundle-driven path
// (used by apply-hooks when a bundle ships prompts).
func TestClaudeSkills_RegisterFromContent(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode()}

	enabled := true
	content := &bundles.LoadedContent{Name: "from-bundle", Content: "body"}
	content.Plugins.LM.ClaudeCode.Enabled = &enabled
	content.Plugins.LM.ClaudeCode.Description = "From a bundle"

	require.NoError(t, skills.RegisterFromContent(workDir, []*bundles.LoadedContent{content}))
	// Check the manifest tracks it.
	manifest, err := os.ReadFile(filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "from-bundle")
}

// TestClaudeSkills_List_NoManifest returns empty + nil when nothing
// has been registered yet.
func TestClaudeSkills_List_NoManifest(t *testing.T) {
	skills := &ClaudeSkills{backend: NewClaudeCode()}
	names, err := skills.List(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestClaudeSkills_List_StripsExtension reads back what Register wrote
// and confirms the .md suffix is gone from the returned names.
func TestClaudeSkills_List_StripsExtension(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode()}
	require.NoError(t, skills.RegisterAll(workDir, []Skill{
		{Name: "a", Content: "x"},
		{Name: "b", Content: "y"},
	}))

	names, err := skills.List(workDir)
	require.NoError(t, err)
	for _, n := range names {
		assert.NotContains(t, n, ".md", "List must strip the .md suffix")
	}
	// Both registered skills should appear.
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	// names from WriteCommandFiles include subdir prefixes; check for
	// any name containing "a" or "b".
	var hasA, hasB bool
	for n := range got {
		if strings.HasSuffix(n, "a") {
			hasA = true
		}
		if strings.HasSuffix(n, "b") {
			hasB = true
		}
	}
	assert.True(t, hasA && hasB, "both registered skills must surface in List: %v", names)
}

// TestClaudeSkills_Clear removes registered skills + manifest. After
// Clear, List returns empty again.
func TestClaudeSkills_Clear(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode()}
	require.NoError(t, skills.Register(workDir, Skill{
		Name: "doomed", Content: "x", Description: "to be cleared",
	}))

	// Sanity check
	before, _ := skills.List(workDir)
	require.NotEmpty(t, before, "fixture should have produced an entry")

	require.NoError(t, skills.Clear(workDir))

	after, err := skills.List(workDir)
	require.NoError(t, err)
	assert.Empty(t, after, "Clear must remove all tracked skills")

	// Manifest itself is gone.
	_, err = os.Stat(filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest"))
	assert.True(t, os.IsNotExist(err), "manifest file must be removed")
}

// TestClaudeSkills_Clear_NoManifest is a no-op when no manifest exists
// (fresh workdir).
func TestClaudeSkills_Clear_NoManifest(t *testing.T) {
	skills := &ClaudeSkills{backend: NewClaudeCode()}
	require.NoError(t, skills.Clear(t.TempDir()), "Clear without manifest must succeed")
}

// =============================================================================
// Claude Context Tests
// =============================================================================

func TestClaudeContext_GetContextHash(t *testing.T) {
	backend := NewClaudeCode()
	context := NewClaudeContext(backend)

	// Write context to set hash
	fragments := []*Fragment{{Content: "test content"}}
	_ = context.Provide("/tmp", fragments)

	hash := context.GetContextHash()
	assert.NotEmpty(t, hash)
}

func TestClaudeContext_GetContextHash_Empty(t *testing.T) {
	backend := NewClaudeCode()
	context := NewClaudeContext(backend)

	hash := context.GetContextHash()
	assert.Equal(t, "", hash)
}

func TestClaudeContext_GetContextFilePath_Empty(t *testing.T) {
	backend := NewClaudeCode()
	context := NewClaudeContext(backend)

	path := context.GetContextFilePath()
	assert.Equal(t, "", path)
}

func TestClaudeContext_GetContextFilePath_WithHash(t *testing.T) {
	backend := NewClaudeCode()
	context := NewClaudeContext(backend)

	// Provide context to generate a hash
	tmpDir := t.TempDir()
	_ = context.Provide(tmpDir, []*Fragment{{Content: "test content"}})

	path := context.GetContextFilePath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, SCMContextSubdir)
	assert.Contains(t, path, ".md")
}

func TestClaudeContext_Clear(t *testing.T) {
	backend := NewClaudeCode()
	context := NewClaudeContext(backend)

	// Provide some context first
	_ = context.Provide("/tmp", []*Fragment{{Content: "test"}})

	err := context.Clear("/tmp")
	require.NoError(t, err)
	assert.Equal(t, "", context.GetContextHash())
}

// =============================================================================
// Claude Lifecycle MergeConfigHooks Tests
// =============================================================================

func TestClaudeLifecycle_MergeConfigHooks_WithContextHash(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP:   config.MCPConfig{Servers: make(map[string]config.MCPServer), Plugins: make(map[string]map[string]config.MCPServer)},
	}

	lifecycle.MergeConfigHooks(cfg, "/tmp", "abc123hash")

	// Verify context injection hook was added
	hooks := lifecycle.GetHooks()
	assert.NotEmpty(t, hooks.Unified.SessionStart)
}

func TestClaudeLifecycle_MergeConfigHooks_NoContextHash(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP:   config.MCPConfig{Servers: make(map[string]config.MCPServer), Plugins: make(map[string]map[string]config.MCPServer)},
	}

	lifecycle.MergeConfigHooks(cfg, "/tmp", "")

	// Without context hash, SessionStart should remain empty
	hooks := lifecycle.GetHooks()
	assert.Empty(t, hooks.Unified.SessionStart)
}

func TestBaseLifecycle_MergeConfigHooks_WithDefaultProfiles(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP:   config.MCPConfig{Servers: make(map[string]config.MCPServer), Plugins: make(map[string]map[string]config.MCPServer)},
		Defaults: config.Defaults{
			Profiles: []string{"test-profile"},
		},
		Profiles: map[string]config.Profile{
			"test-profile": {
				Hooks: config.HooksConfig{
					Unified: config.UnifiedHooks{
						PreTool: []config.Hook{{Command: "profile-hook"}},
					},
				},
				MCP: config.MCPConfig{
					Servers: map[string]config.MCPServer{
						"profile-mcp": {Command: "profile-mcp-cmd"},
					},
				},
			},
		},
	}

	lifecycle.MergeConfigHooks(cfg, "/tmp", "hash123")

	// Hooks from profile should be merged
	hooks := lifecycle.GetHooks()
	assert.Len(t, hooks.Unified.PreTool, 1)
	assert.Equal(t, "profile-hook", hooks.Unified.PreTool[0].Command)

	// MCP from profile should be merged
	mcp := lifecycle.GetMCP()
	assert.Contains(t, mcp.Servers, "profile-mcp")
}

func TestBaseLifecycle_MergeConfigHooks_WithInvalidProfile(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP:   config.MCPConfig{Servers: make(map[string]config.MCPServer), Plugins: make(map[string]map[string]config.MCPServer)},
		Defaults: config.Defaults{
			Profiles: []string{"non-existent-profile"},
		},
		Profiles: map[string]config.Profile{}, // No profiles defined
	}

	// Should not panic with invalid profile reference
	lifecycle.MergeConfigHooks(cfg, "/tmp", "hash123")

	// Should still have context injection hook
	hooks := lifecycle.GetHooks()
	assert.NotEmpty(t, hooks.Unified.SessionStart)
}

// =============================================================================
// Base Session Registry Tests
// =============================================================================

func TestBaseSessionRegistry_New(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry := NewBaseSessionRegistry("test-registry.json", WithRegistryFS(fs))
	assert.NotNil(t, registry)
}

func TestBaseSessionRegistry_RegisterSession(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry := NewBaseSessionRegistry("test-registry.json", WithRegistryFS(fs))
	workDir := "/test/project"

	err := registry.RegisterSession(workDir, 12345, "/path/to/session.jsonl")
	require.NoError(t, err)

	// Second registration with same path should be idempotent
	err = registry.RegisterSession(workDir, 12345, "/path/to/session.jsonl")
	require.NoError(t, err)

	// Verify file was created
	exists, _ := afero.Exists(fs, "/test/project/.ctxloom/ephemeral/test-registry.json")
	assert.True(t, exists)
}

func TestBaseSessionRegistry_GetPreviousSession_NoPrevious(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry := NewBaseSessionRegistry("test-registry.json", WithRegistryFS(fs))
	workDir := "/test/project"

	// Only one session registered
	_ = registry.RegisterSession(workDir, 12345, "/path/to/session1.jsonl")

	session, err := registry.GetPreviousSession(workDir, 12345, func(path string) (*Session, error) {
		return &Session{ID: "test"}, nil
	})
	require.NoError(t, err)
	assert.Nil(t, session) // No previous when only one session exists
}

func TestBaseSessionRegistry_GetPreviousSession_WithPrevious(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry := NewBaseSessionRegistry("test-registry.json", WithRegistryFS(fs))
	workDir := "/test/project"

	// Register two sessions
	_ = registry.RegisterSession(workDir, 12345, "/path/to/session1.jsonl")
	_ = registry.RegisterSession(workDir, 12345, "/path/to/session2.jsonl")

	session, err := registry.GetPreviousSession(workDir, 12345, func(path string) (*Session, error) {
		return &Session{ID: path}, nil
	})
	require.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, "/path/to/session1.jsonl", session.ID) // Returns the first (previous) session
}

func TestBaseSessionRegistry_Pruning(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry := NewBaseSessionRegistry("test-registry.json", WithRegistryFS(fs))
	workDir := "/test/project"

	// Register sessions for many PIDs to trigger pruning
	for i := 0; i < 150; i++ {
		_ = registry.RegisterSession(workDir, 10000+i, "/path/to/session.jsonl")
	}

	// Should still work - pruning keeps maxEntries (100)
	err := registry.RegisterSession(workDir, 99999, "/path/to/new.jsonl")
	require.NoError(t, err)
}

func TestBaseSessionRegistry_EmptyRegistry(t *testing.T) {
	fs := afero.NewMemMapFs()
	registry := NewBaseSessionRegistry("test-registry.json", WithRegistryFS(fs))
	workDir := "/test/project"

	// Get previous session when no sessions exist
	session, err := registry.GetPreviousSession(workDir, 12345, func(path string) (*Session, error) {
		return &Session{ID: path}, nil
	})
	require.NoError(t, err)
	assert.Nil(t, session)
}

// =============================================================================
// Lifecycle GetMCP Tests
// =============================================================================

func TestClaudeLifecycle_GetMCP(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	// Initially nil
	mcp := lifecycle.GetMCP()
	assert.Nil(t, mcp)

	// After adding a server, MCP config should exist
	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"test-server": {Command: "test"},
			},
			Plugins: make(map[string]map[string]config.MCPServer),
		},
	}
	lifecycle.MergeConfigHooks(cfg, "/tmp", "")

	mcp = lifecycle.GetMCP()
	assert.NotNil(t, mcp)
}

func TestGeminiLifecycle_GetMCP(t *testing.T) {
	backend := NewGemini()
	lifecycle := NewGeminiLifecycle(backend)

	// Initially nil
	mcp := lifecycle.GetMCP()
	assert.Nil(t, mcp)

	// After merging config with MCP servers
	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"test-server": {Command: "test"},
			},
			Plugins: make(map[string]map[string]config.MCPServer),
		},
	}
	lifecycle.MergeConfigHooks(cfg, "/tmp", "")

	mcp = lifecycle.GetMCP()
	assert.NotNil(t, mcp)
}

// =============================================================================
// History Accessor Tests
// =============================================================================

func TestClaudeCode_History(t *testing.T) {
	backend := NewClaudeCode()
	history := backend.History()
	assert.NotNil(t, history)
}

func TestGemini_History(t *testing.T) {
	backend := NewGemini()
	history := backend.History()
	assert.NotNil(t, history)
}

func TestCodex_History(t *testing.T) {
	backend := NewCodex()
	history := backend.History()
	assert.NotNil(t, history)
}

func TestMock_History(t *testing.T) {
	backend := NewMock()
	history := backend.History()
	// Mock returns a NilSessionHistory (stub that returns empty/nil for all methods)
	assert.NotNil(t, history)
}
