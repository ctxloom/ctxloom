package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	content.LLM.ClaudeCode.Enabled = &enabled
	content.LLM.ClaudeCode.Description = "From a bundle"

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

	hooks := lifecycle.GetHooks()
	// Without a context hash, the context-injection hook is omitted...
	for _, h := range hooks.Unified.SessionStart {
		assert.NotContains(t, h.Command, "inject-context",
			"context-injection hook must not be added when contextHash is empty")
	}
	// ...but the bundle-shipped SessionStart hooks (e.g. `hook session-bind`) are
	// still assembled. Setup omitting these is what left every `ctxloom run`
	// session launching without forward-bind.
	var hasBind bool
	for _, h := range hooks.Unified.SessionStart {
		if strings.Contains(h.Command, "session-bind") {
			hasBind = true
		}
	}
	assert.True(t, hasBind, "bundle `hook session-bind` hook should be present even without a context hash")
}

func TestBaseLifecycle_MergeConfigHooks_WithDefaultProfiles(t *testing.T) {
	backend := NewClaudeCode()
	lifecycle := NewClaudeLifecycle(backend)

	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		MCP:   config.MCPConfig{Servers: make(map[string]config.MCPServer), Plugins: make(map[string]map[string]config.MCPServer)},
		Profiles: config.ProfilesConfig{
			Defaults: []string{"test-profile"},
			Definitions: map[string]config.Profile{
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
		Profiles: config.ProfilesConfig{
			Defaults:    []string{"non-existent-profile"},
			Definitions: map[string]config.Profile{}, // No profiles defined
		},
	}

	// Should not panic with invalid profile reference
	lifecycle.MergeConfigHooks(cfg, "/tmp", "hash123")

	// Should still have context injection hook
	hooks := lifecycle.GetHooks()
	assert.NotEmpty(t, hooks.Unified.SessionStart)
}

// sessionStartCommands returns the SessionStart hook commands in order.
func sessionStartCommands(h config.UnifiedHooks) []string {
	cmds := make([]string, 0, len(h.SessionStart))
	for _, hook := range h.SessionStart {
		cmds = append(cmds, hook.Command)
	}
	return cmds
}

// TestAssembleManagedHooks_IncludesProfileSessionStartHook locks in the
// writer-parity contract: a profile-shipped SessionStart hook must appear in
// the set assembled by AssembleManagedHooks (the operations.ApplyHooks path),
// not only the `ctxloom run` Setup path. Before this, Setup merged default-
// profile hooks and apply-hooks did not, so the next apply-hooks reconcile
// dropped the profile hook — the same drop-on-clobber class that broke
// forward-bind.
func TestAssembleManagedHooks_IncludesProfileSessionStartHook(t *testing.T) {
	cfg := &config.Config{
		Hooks: config.HooksConfig{Plugins: make(map[string]config.BackendHooks)},
		Profiles: config.ProfilesConfig{
			Defaults: []string{"p"},
			Definitions: map[string]config.Profile{
				"p": {Hooks: config.HooksConfig{Unified: config.UnifiedHooks{
					SessionStart: []config.Hook{{Command: "profile-session-start", Type: "command"}},
				}}},
			},
		},
	}

	assembled := AssembleManagedHooks(cfg, "/tmp", "")

	assert.Contains(t, sessionStartCommands(assembled.Unified), "profile-session-start",
		"profile-shipped SessionStart hook must be in the assembled set used by apply-hooks")
}

// TestAssembleManagedHooks_MatchesSetup is the core regression guard against the
// two writers diverging: the set MergeConfigHooks (Setup) writes must equal the
// set AssembleManagedHooks (apply-hooks) writes, for a config exercising both
// config-level and profile-level SessionStart hooks. Divergence here is exactly
// what lets WriteSettings' remove-then-add reconcile drop a managed hook.
func TestAssembleManagedHooks_MatchesSetup(t *testing.T) {
	newCfg := func() *config.Config {
		return &config.Config{
			Hooks: config.HooksConfig{
				Unified: config.UnifiedHooks{
					SessionStart: []config.Hook{{Command: "config-session-start", Type: "command"}},
				},
				Plugins: make(map[string]config.BackendHooks),
			},
			MCP: config.MCPConfig{Servers: make(map[string]config.MCPServer), Plugins: make(map[string]map[string]config.MCPServer)},
			Profiles: config.ProfilesConfig{
				Defaults: []string{"p"},
				Definitions: map[string]config.Profile{
					"p": {Hooks: config.HooksConfig{Unified: config.UnifiedHooks{
						SessionStart: []config.Hook{{Command: "profile-session-start", Type: "command"}},
					}}},
				},
			},
		}
	}

	// Setup path.
	lifecycle := NewClaudeLifecycle(NewClaudeCode())
	lifecycle.MergeConfigHooks(newCfg(), "/tmp", "hash123")
	setupCmds := sessionStartCommands(lifecycle.GetHooks().Unified)

	// apply-hooks path.
	assembled := AssembleManagedHooks(newCfg(), "/tmp", "hash123")
	applyCmds := sessionStartCommands(assembled.Unified)

	assert.Equal(t, setupCmds, applyCmds,
		"Setup (MergeConfigHooks) and apply-hooks (AssembleManagedHooks) must produce an identical SessionStart set")
}

// TestAssembleManagedHooks_DoesNotMutateConfig guards the duplication fix:
// apply-hooks calls AssembleManagedHooks once per backend in a loop. If it
// aliased and appended to cfg.Hooks (the old `hooksCfg := &freshCfg.Hooks`
// pattern), the second backend would accumulate duplicate bundle/inject hooks.
// Asserting two calls return identical-length sets proves it builds fresh.
func TestAssembleManagedHooks_DoesNotMutateConfig(t *testing.T) {
	cfg := &config.Config{
		Hooks: config.HooksConfig{
			Unified: config.UnifiedHooks{SessionStart: []config.Hook{{Command: "config-session-start"}}},
			Plugins: make(map[string]config.BackendHooks),
		},
	}

	first := AssembleManagedHooks(cfg, "/tmp", "hash123")
	second := AssembleManagedHooks(cfg, "/tmp", "hash123")

	assert.Equal(t, len(first.Unified.SessionStart), len(second.Unified.SessionStart),
		"repeated calls must not accumulate hooks via shared config state")
	assert.Len(t, cfg.Hooks.Unified.SessionStart, 1,
		"AssembleManagedHooks must not mutate the caller's config.Hooks")
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
