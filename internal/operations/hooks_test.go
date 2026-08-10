// Package operations tests for ApplyHooks verify the hook application system.
//
// Hooks are the glue between ctxloom configuration and LLM backend settings files.
// ApplyHooks transforms ctxloom's unified hook config into backend-specific formats
// (e.g., .claude/settings.json, .agents/hooks.json) and regenerates context.
//
// # What ApplyHooks Does
//
//  1. Reads ctxloom config (hooks, MCP servers, profiles)
//  2. Writes backend-specific settings files with hooks
//  3. Writes .mcp.json with MCP server configs
//  4. Optionally regenerates context file from active profiles
//
// # Test Injection Patterns
//
// Tests inject dependencies to avoid real filesystem operations:
//   - ConfigLoader: Function returning mock config instead of reading disk
//   - FS: afero virtual filesystem for settings file writes
//   - WorkDir: Explicit working directory instead of git root detection
//   - BundleLoaderFS: Separate FS for bundle reading in context regeneration
//
// # Fault Tolerance
//
// Context regeneration is intentionally fault-tolerant:
//   - Missing bundles/fragments produce warnings, not errors
//   - Unresolvable parent profiles are skipped
//   - The hook application still succeeds even if context has issues
//
// This ensures the user's LLM session starts even with partial config problems.
package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ==========================================================================
// Request/Result struct tests
// ==========================================================================
//
// These tests verify the data structures used for hook operations.
// They ensure default values and field presence.

func TestApplyHooksRequest_Defaults(t *testing.T) {
	req := ApplyHooksRequest{}

	assert.Empty(t, req.Backend)
	assert.False(t, req.RegenerateContext)
}

func TestApplyHooksRequest_AllBackends(t *testing.T) {
	req := ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: true,
	}

	assert.Equal(t, "all", req.Backend)
	assert.True(t, req.RegenerateContext)
}

func TestApplyHooksRequest_ClaudeCode(t *testing.T) {
	req := ApplyHooksRequest{
		Backend: "claude-code",
	}

	assert.Equal(t, "claude-code", req.Backend)
}

func TestApplyHooksRequest_Antigravity(t *testing.T) {
	req := ApplyHooksRequest{
		Backend: "antigravity",
	}

	assert.Equal(t, "antigravity", req.Backend)
}

func TestApplyHooksResult_Fields(t *testing.T) {
	result := ApplyHooksResult{
		Status:      "applied",
		Backends:    []string{"claude-code", "antigravity"},
		ContextHash: "abc123",
	}

	assert.Equal(t, "applied", result.Status)
	assert.Len(t, result.Backends, 2)
	assert.Equal(t, "abc123", result.ContextHash)
}

func TestApplyHooksResult_NoContextHash(t *testing.T) {
	result := ApplyHooksResult{
		Status:   "applied",
		Backends: []string{"claude-code"},
	}

	assert.Equal(t, "applied", result.Status)
	assert.Empty(t, result.ContextHash)
}

func TestApplyHooksRequest_BackendValues(t *testing.T) {
	validBackends := []string{"all", "claude-code", "antigravity", ""}

	for _, backend := range validBackends {
		req := ApplyHooksRequest{
			Backend: backend,
		}
		assert.NotNil(t, req)
	}
}

func TestApplyHooksRequest_FSField(t *testing.T) {
	fs := afero.NewMemMapFs()
	req := ApplyHooksRequest{
		FS: fs,
	}

	assert.NotNil(t, req.FS)
}

// ==========================================================================
// WriteSettings tests
// ==========================================================================
//
// deliverManagedSettings materializes a backend's settings + MCP surfaces into dir
// via the surface selection — the test replacement for the removed WriteSettings
// facade. manageStatusline mirrors the old WithStatusLineDisabled inverse.
func deliverManagedSettings(t *testing.T, backend string, hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, manageStatusline bool, dir string, fs afero.Fs) {
	t.Helper()
	set := backends.BuildSurfaces(backend, agent.SurfaceInputs{
		Hooks:            hooks,
		MCP:              mcp,
		BundleMCP:        bundleMCP,
		ManageStatusline: manageStatusline,
	}, fs)
	_, _, errs := agent.Select(set).WithSettings(agent.SettingsWriteUnsafeFile).WithMCP(agent.MCPWriteUnsafeFile).DeliverUnder(dir)
	require.Empty(t, errs)
}

// TestManagedSettings_ClaudeCode verifies Claude Code settings file creation via
// the settings + MCP surfaces. Claude Code reads .claude/settings.json for hooks.
func TestManagedSettings_ClaudeCode(t *testing.T) {
	fs := afero.NewMemMapFs()

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	deliverManagedSettings(t, "claude-code", hooks, nil, nil, true, "/project", fs)

	// Verify settings file was created
	exists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists, "settings.json should be created")

	// Verify MCP config file was created (even if empty)
	exists, err = afero.Exists(fs, "/project/.mcp.json")
	require.NoError(t, err)
	assert.True(t, exists, ".mcp.json should be created")

	// Read and verify content contains hooks
	content, err := afero.ReadFile(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.Contains(t, string(content), "hooks")
	assert.Contains(t, string(content), "SessionStart")
}

// TestManagedSettings_Antigravity tests writing Antigravity hooks with FS injection.
// TestManagedSettings_UnsupportedBackend tests that unsupported backends deliver
// nothing (EmptySurfaceSet) rather than erroring.
func TestManagedSettings_UnsupportedBackend(t *testing.T) {
	set := backends.BuildSurfaces("unknown-backend", agent.SurfaceInputs{}, afero.NewMemMapFs())
	_, _, errs := agent.Select(set).WithEverything().DeliverUnder("/project")
	assert.Empty(t, errs, "unsupported backend materializes nothing")
}

// TestManagedSettings_PreservesExistingSettings verifies that user customizations survive.
//
// NON-OBVIOUS: When ctxloom writes hooks to settings.json, it MERGES with existing
// content rather than overwriting. User's custom settings (like theme preferences)
// are preserved. Only ctxloom-managed sections (hooks, MCP servers) are updated.
//
// This is important because users may have manually configured settings that
// they don't want ctxloom to destroy on each hook application.
func TestManagedSettings_PreservesExistingSettings(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create existing settings with custom user config
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	existingContent := `{
  "customSetting": "userValue",
  "anotherSetting": 123
}`
	require.NoError(t, afero.WriteFile(fs, "/project/.claude/settings.json", []byte(existingContent), 0644))

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	deliverManagedSettings(t, "claude-code", hooks, nil, nil, true, "/project", fs)

	// Read and verify user settings are preserved
	content, err := afero.ReadFile(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.Contains(t, string(content), "customSetting")
	assert.Contains(t, string(content), "userValue")
	assert.Contains(t, string(content), "hooks")
}

// ==========================================================================
// Context file tests
// ==========================================================================
//
// Context files are the assembled markdown content that gets fed to the LLM.
// They contain fragments from profiles, deduplicated and ordered.

// TestWriteContextFile verifies that fragments are assembled into a context file.
// The file is stored in .ctxloom/cache/context/{hash}.md where hash is content-based.
func TestWriteContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()

	fragments := []*agent.Fragment{
		{Name: "frag1", Content: "First fragment content"},
		{Name: "frag2", Content: "Second fragment content"},
	}

	hash, err := agent.WriteContextFile("/project", fragments,
		agent.WithContextFS(fs))
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Verify context file was created
	contextPath := paths.CachePath(testBaseDir) + "/context/" + hash + ".md"
	exists, err := afero.Exists(fs, contextPath)
	require.NoError(t, err)
	assert.True(t, exists, "context file should be created")

	// Read and verify content
	content, err := afero.ReadFile(fs, contextPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "First fragment content")
	assert.Contains(t, string(content), "Second fragment content")
}

// TestWriteContextFile_Empty pins the distinction WriteContextFile must draw:
// NO fragments is legitimately nothing to deliver, while fragments that exist
// but assemble to zero bytes is a delivery failure. Reporting both
// as a silent success made "no context configured" indistinguishable from
// "every fragment resolved empty".
func TestWriteContextFile_Empty(t *testing.T) {
	fs := afero.NewMemMapFs()

	hash, err := agent.WriteContextFile("/project", nil,
		agent.WithContextFS(fs))
	require.NoError(t, err, "no fragments at all: nothing was asked for")
	assert.Empty(t, hash)

	// Fragments that exist but carry no content: asked for context, got none.
	fragments := []*agent.Fragment{
		{Name: "empty", Content: ""},
	}
	hash, err = agent.WriteContextFile("/project", fragments,
		agent.WithContextFS(fs))
	require.ErrorIs(t, err, agent.ErrNoContext)
	assert.Empty(t, hash)
}

// ==========================================================================
// ApplyHooks integration tests
// ==========================================================================
//
// These tests verify the full ApplyHooks operation with mock config loaders.
// They cover backend selection, context regeneration, and error handling.

// TestApplyHooks_ClaudeCodeOnly verifies backend-specific hook application.
// When Backend="claude-code", only Claude settings are written.
func TestApplyHooks_ClaudeCodeOnly(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	// Create a mock config loader
	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{
						{Command: "echo test", Type: "command"},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, result.Backends, "claude-code")
	assert.NotContains(t, result.Backends, "antigravity")

	// Verify Claude Code settings file was created
	exists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify Antigravity hooks file was NOT created
	exists, err = afero.Exists(fs, "/project/.agents/hooks.json")
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestApplyHooks_AllBackends tests applying hooks to all backends.
func TestApplyHooks_AllBackends(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{
						{Command: "echo hello", Type: "command"},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "all",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.Len(t, result.Backends, 4)
	assert.Contains(t, result.Backends, "claude-code")
	assert.Contains(t, result.Backends, "codex")
	assert.Contains(t, result.Backends, "kiro")
	assert.Contains(t, result.Backends, "opencode")

	// Verify each backend's settings file was created
	exists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = afero.Exists(fs, "/project/.codex/config.toml")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = afero.Exists(fs, "/project/.kiro/agents/ctxloom.json")
	require.NoError(t, err)
	assert.True(t, exists)

	// opencode has no ctxloom hook mechanism, but its MCP (the auto-registered
	// ctxloom server) is written into opencode.json.
	exists, err = afero.Exists(fs, "/project/opencode.json")
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestApplyHooks_DefaultBackend tests that empty backend defaults to "all".
func TestApplyHooks_DefaultBackend(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{}, nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "", // empty should default to "all"
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
	})

	require.NoError(t, err)
	assert.Len(t, result.Backends, 4)
}

// TestApplyHooks_ConfigLoadError tests error handling when config load fails.
func TestApplyHooks_ConfigLoadError(t *testing.T) {
	fs := afero.NewMemMapFs()

	mockConfigLoader := func() (*config.Config, error) {
		return nil, fmt.Errorf("config file not found")
	}

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      "/project",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}

// TestApplyHooks_WithMCPServers tests that MCP servers are written correctly.
func TestApplyHooks_WithMCPServers(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			MCP: wire.MCPConfig{
				Servers: map[string]wire.MCPServer{
					"test-server": {
						Command: "test-cmd",
						Args:    []string{"arg1", "arg2"},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)

	// Verify MCP config file was created
	exists, err := afero.Exists(fs, "/project/.mcp.json")
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify MCP config contains the server
	content, err := afero.ReadFile(fs, "/project/.mcp.json")
	require.NoError(t, err)
	assert.Contains(t, string(content), "test-server")
	assert.Contains(t, string(content), "test-cmd")
}

// ==========================================================================
// checkHookTargetScope tests
// ==========================================================================
//
// `manage hooks install` run from $HOME (no git root, no CTXLOOM_ROOT) used to
// silently write Claude Code's user-GLOBAL settings.json instead of a
// project's: workDir fell back to bare cwd == $HOME, and
// claude.ProjectSettingsPath(workDir) == claude.GlobalSettingsPath() in that
// case. That duplicated ctxloom's injected context (and the /clear banner)
// into every project. These tests pin the fix: ApplyHooks now refuses that
// collision unless Force is set, and separately warns (but proceeds) when
// workDir resolved via the bare-cwd fallback for any other reason.
//
// All four use testsupport.Isolate/ProjectDir so HOME is a fresh temp dir and
// the real ~/.claude is never at risk, on top of the injected memFS.

// TestApplyHooks_RefusesHomeCollision is the canonical red case: WorkDir set
// to HOME itself makes claude.ProjectSettingsPath(WorkDir) resolve onto
// claude.GlobalSettingsPath(). Before the fix this silently succeeded and
// wrote to $HOME/.claude/settings.json; it must now error and write nothing.
func TestApplyHooks_RefusesHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      home, // == the resolved Claude Code GLOBAL settings scope
	})
	require.Error(t, err, "installing hooks with WorkDir==HOME must be refused")
	assert.Contains(t, err.Error(), "user-global settings")

	exists, existsErr := afero.Exists(fs, claude.ProjectSettingsPath(home))
	require.NoError(t, existsErr)
	assert.False(t, exists, "a refused apply must not write settings under HOME")
}

// TestApplyHooks_ForceOverridesHomeCollision proves --force (Force:true) is
// the deliberate escape hatch: the same collision now warns loudly instead of
// erroring, and the apply proceeds and writes.
func TestApplyHooks_ForceOverridesHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	var result *ApplyHooksResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend:      "claude-code",
			FS:           fs,
			ConfigLoader: mockConfigLoader,
			WorkDir:      home,
			Force:        true,
		})
		require.NoError(t, err, "Force:true must let the collision proceed")
	})
	require.NotNil(t, result)
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, stderr, "user-global settings", "Force must still warn loudly, not silently proceed")

	exists, err := afero.Exists(fs, claude.ProjectSettingsPath(home))
	require.NoError(t, err)
	assert.True(t, exists, "--force must actually write the settings file")
}

// TestApplyHooks_RefusesCodexHomeCollision is the codex hook-scope guard's
// canonical red case, the codex sibling of
// TestApplyHooks_RefusesHomeCollision: WorkDir set to HOME makes
// codex.ProjectHome(WorkDir) resolve onto codex.GlobalHome() too — codex's
// whole config.toml/prompts/skills home, not just claude's settings.json.
func TestApplyHooks_RefusesCodexHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "codex",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      home,
	})
	require.Error(t, err, "installing hooks with WorkDir==HOME must be refused for codex too")
	assert.Contains(t, err.Error(), "codex's global home")

	exists, existsErr := afero.Exists(fs, filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, existsErr)
	assert.False(t, exists, "a refused apply must not write codex's config.toml under HOME")
}

// TestApplyHooks_ForceOverridesCodexHomeCollision proves --force also covers
// the codex collision, writing anyway with a loud warning.
func TestApplyHooks_ForceOverridesCodexHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	var result *ApplyHooksResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend:      "codex",
			FS:           fs,
			ConfigLoader: mockConfigLoader,
			WorkDir:      home,
			Force:        true,
		})
		require.NoError(t, err, "Force:true must let the codex collision proceed")
	})
	require.NotNil(t, result)
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, stderr, "codex's global home", "Force must still warn loudly, not silently proceed")

	exists, err := afero.Exists(fs, filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	assert.True(t, exists, "--force must actually write codex's config.toml")
}

// TestApplyHooks_RefusesKiroHomeCollision is the kiro hook-scope guard's
// canonical red case, the kiro sibling of
// TestApplyHooks_RefusesHomeCollision / TestApplyHooks_RefusesCodexHomeCollision:
// WorkDir set to HOME makes kiro's project-scoped .kiro dir
// (filepath.Join(WorkDir, ".kiro")) resolve onto kiro's global home
// (kiroHome(): $KIRO_HOME, else ~/.kiro) too — kiro's whole
// agents/settings/steering home, not just one file. This is the SAME
// collision class found for claude and codex;
// kiro was unaudited until now (see kiro.go's own doc: "kiro-cli resolves
// the materialized WORKSPACE .kiro/agents/<name>.json over any global
// ~/.kiro/agents copy" — that precedence is moot when workDir==HOME, because
// there IS no separate workspace copy at that point, only the global one).
func TestApplyHooks_RefusesKiroHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "kiro",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      home,
	})
	require.Error(t, err, "installing hooks with WorkDir==HOME must be refused for kiro too")
	assert.Contains(t, err.Error(), "kiro's global home")

	exists, existsErr := afero.Exists(fs, filepath.Join(home, ".kiro", "agents", "ctxloom.json"))
	require.NoError(t, existsErr)
	assert.False(t, exists, "a refused apply must not write kiro's agent config under HOME")
}

// TestApplyHooks_ForceOverridesKiroHomeCollision proves --force also covers
// the kiro collision, writing anyway with a loud warning.
func TestApplyHooks_ForceOverridesKiroHomeCollision(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	var result *ApplyHooksResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend:      "kiro",
			FS:           fs,
			ConfigLoader: mockConfigLoader,
			WorkDir:      home,
			Force:        true,
		})
		require.NoError(t, err, "Force:true must let the kiro collision proceed")
	})
	require.NotNil(t, result)
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, stderr, "kiro's global home", "Force must still warn loudly, not silently proceed")

	exists, err := afero.Exists(fs, filepath.Join(home, ".kiro", "agents", "ctxloom.json"))
	require.NoError(t, err)
	assert.True(t, exists, "--force must actually write kiro's agent config")
}

// TestApplyHooks_TargetScopeGuardAppliesToAnyRegisteredBackend is the
// flow-level proof: the target-scope guard is a property of the
// internal/lm/backends descriptor table, not a hardcoded claude/codex/kiro
// list operations maintains its own copy of. Before this fix,
// checkHookTargetScope was a literal 3-way if/else naming exactly those three backends and calling
// claude/codex/kiro package functions directly (the ADR-0026 violation) — a
// FOURTH backend with the identical $HOME==global collision class got NO
// protection no matter how it registered itself, because operations' copy of
// "which backends have this collision" could only ever be edited by hand.
//
// This test registers a synthetic backend nobody has hardcoded anywhere
// (backends.RegisterHookGlobalScopeForTesting, the exact seam a real
// backend's descriptor uses in registry.go) and proves ApplyHooks refuses the
// $HOME collision for it — generalizing the claude/codex/kiro
// fix to "any registered backend", the property the old hardcoded branch
// could not have: it would have silently proceeded for this name.
func TestApplyHooks_TargetScopeGuardAppliesToAnyRegisteredBackend(t *testing.T) {
	const fakeBackend = "t12-flow-test-engine"
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	// A collision class shaped exactly like claude/codex/kiro's: the
	// "project" path is a workDir join that happens to equal the "global"
	// path whenever workDir == HOME.
	backends.RegisterHookGlobalScopeForTesting(fakeBackend,
		func(workDir string) (string, string, error) {
			return filepath.Join(workDir, ".t12fake", "settings.json"), filepath.Join(home, ".t12fake", "settings.json"), nil
		},
		"the T12 fake engine's global settings",
	)
	t.Cleanup(func() { backends.UnregisterForTesting(fakeBackend) })

	_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      fakeBackend,
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      home,
	})
	require.Error(t, err, "a backend registered with a hookGlobalScopePaths collision must be refused just like claude/codex/kiro, with no operations-side edit for this backend name")
	assert.Contains(t, err.Error(), "the T12 fake engine's global settings")

	exists, existsErr := afero.Exists(fs, filepath.Join(home, ".t12fake", "settings.json"))
	require.NoError(t, existsErr)
	assert.False(t, exists, "a refused apply must not write the fake engine's settings under HOME")
}

// TestApplyHooks_SubdirOfHomeIsNotACollision is the negative control: a real
// project living under HOME (e.g. ~/code/myproject) is not the global-scope
// collision — only WorkDir==HOME itself is.
func TestApplyHooks_SubdirOfHomeIsNotACollision(t *testing.T) {
	home := testsupport.Isolate(t)
	projectDir := filepath.Join(home, "project")
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      projectDir,
	})
	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)

	exists, err := afero.Exists(fs, claude.ProjectSettingsPath(projectDir))
	require.NoError(t, err)
	assert.True(t, exists, "a project subdirectory of HOME must apply normally")
}

// TestApplyHooks_WarnsWhenNotInAGitRepository pins the general (non-$HOME)
// case: installing from any non-repo, non-CTXLOOM_ROOT directory now fires
// the same "not in a git repository" advisory `ctxloom run` already prints —
// the acute $HOME collision above is just the sharpest instance of this
// broader silent-scope problem. WorkDir is left unset so ApplyHooks resolves
// it itself (the branch RootFromFallback actually gates); an explicitly
// injected WorkDir wouldn't exercise this path.
func TestApplyHooks_WarnsWhenNotInAGitRepository(t *testing.T) {
	testsupport.ProjectDir(t) // isolates HOME/env and chdirs to a fresh non-git temp dir
	fs := afero.NewMemMapFs()
	mockConfigLoader := func() (*config.Config, error) { return &config.Config{}, nil }

	stderr := captureStderr(t, func() {
		_, err := ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend:      "claude-code",
			FS:           fs,
			ConfigLoader: mockConfigLoader,
		})
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "not in a git repository")
}

// ==========================================================================
// Context regeneration tests
// ==========================================================================
//
// These tests verify that ApplyHooks properly regenerates context when
// RegenerateContext=true. The context is assembled from active profiles.

// TestApplyHooks_RegenerateContextEmpty verifies no crash with empty profiles.
// An empty context hash is returned when there's nothing to assemble.
func TestApplyHooks_RegenerateContextEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			// No profiles or fragments - regenerateContext should return empty
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		FS:                fs,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	// No PROFILE-sourced fragments, but the always-on builtin isolation
	// fragment still injects unconditionally (see
	// TestAssembleContext_InjectsBuiltinIsolationFragment), so a context file
	// is still written and hashed.
	assert.NotEmpty(t, result.ContextHash)
}

// TestApplyHooks_RegenerateContextWithTags tests regenerateContext with profile tags.
func TestApplyHooks_RegenerateContextWithTags(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Create bundle with tagged fragments
	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  security-rules:
    tags: ["security"]
    content: |
      ## Security Rules
      - Always validate input
  go-patterns:
    tags: ["go", "patterns"]
    content: |
      ## Go Patterns
      - Use interfaces
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "dev.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						SelectTags: []string{"security"},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.NotEmpty(t, result.ContextHash) // Should have context hash since fragments were found
}

// TestApplyHooks_ClaudeCode_NoNativeContextFile pins the guardrail:
// claude's apply-context rides WithContext(Hook) — the settings-carried
// SessionStart inject hook + the regenerated cache file — so apply must NEVER
// also write a native CLAUDE.md (that would DOUBLE the context, the exact
// regression the design forbids). The settings.json hook is still present.
func TestApplyHooks_ClaudeCode_NoNativeContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionStart: []wire.Hook{{Command: "echo test", Type: "command"}},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		FS:                fs,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)

	exists, err := afero.Exists(fs, "/project/CLAUDE.md")
	require.NoError(t, err)
	assert.False(t, exists, "claude apply must never write a native CLAUDE.md alongside the inject hook")

	exists, err = afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists, "the settings.json carrying the inject hook must still be written")
}

// TestApplyHooks_Codex_NoNativeContextFile pins the same guardrail for codex:
// its context surface is Hook-only (no CLAUDE.md-style native file exists for
// codex at all), so apply must write only config.toml — never a stray native
// context file.
func TestApplyHooks_Codex_NoNativeContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{}, nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "codex",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)

	exists, err := afero.Exists(fs, "/project/.codex/config.toml")
	require.NoError(t, err)
	assert.True(t, exists, "codex's config.toml must be written")
}

// TestApplyHooks_RegenerateContextSubstitutesVariables pins parity with
// AssembleContext: profile-declared variables must be substituted into the
// regenerated context file. regenerateContext is a second assembly of the
// same fragments — without substitution the agent-injected context ships
// literal {{var}} tags whenever profiles define variables.
// TestApplyHooks_RegenerateContextFailure_PreservesExistingNativeContext
// pins that a genuine context-regeneration FAILURE (RegenerateContext:
// true, but the regen write itself errors — degraded mode warns and does
// not abort) must never silently strip a native-file backend's
// PREVIOUSLY-installed managed context (.agents/AGENTS.md), and must never
// report status "applied" — the signature "success reported, bytes
// destroyed" bug. This is distinct from the LEGITIMATE RegenerateContext:
// false path (which never attempts regeneration at all, and is unaffected
// by this fix) and from a genuinely-empty fragment set (attempted, no
// error — see hooks.go's maybeRegenerateContext for how the cases differ).
func TestApplyHooks_RegenerateContextFailure_PreservesExistingNativeContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  security-rules:
    tags: ["security"]
    content: |
      ## Security Rules
      - Always validate input
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "dev.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {SelectTags: []string{"security"}},
				},
			},
		}), nil
	}

	agentsMDPath := filepath.Join(tmpDir, "AGENTS.md")

	// First apply: succeeds, installs real managed context.
	result1, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "codex",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})
	require.NoError(t, err)
	require.Equal(t, "applied", result1.Status)
	before, err := os.ReadFile(agentsMDPath)
	require.NoError(t, err, "AGENTS.md must carry the assembled context after the first apply")
	require.Contains(t, string(before), "Always validate input")

	// Force the NEXT regeneration to genuinely fail: WriteContextFile's
	// context-cache directory (workDir/.ctxloom/cache/context) needs
	// .ctxloom/cache to be a directory it can MkdirAll under — the first
	// apply above already created it as one, so replace it with a plain
	// file at that same path component: the real os.MkdirAll call then
	// errors, exactly like a real disk/permission failure would.
	require.NoError(t, os.RemoveAll(filepath.Join(appDir, "cache")))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "cache"), []byte("blocking file"), 0644))

	result2, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "codex",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})
	require.NoError(t, err, "ApplyHooks itself must not hard-fail in degraded mode")
	assert.NotEqual(t, "applied", result2.Status,
		"a genuine context-regeneration failure must never report status \"applied\"")

	after, err := os.ReadFile(agentsMDPath)
	require.NoError(t, err, "the existing native context file must still exist after a failed regeneration")
	assert.Equal(t, string(before), string(after),
		"a failed regeneration must never strip or alter existing native-file managed context")
}

func TestApplyHooks_RegenerateContextSubstitutesVariables(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  greeting:
    content: |
      Hello {{project_name}}.
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						Fragments: []config.FragmentRef{{Name: "test#fragments/greeting"}},
						Variables: map[string]string{"project_name": "ctxloom"},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})

	require.NoError(t, err)
	require.NotEmpty(t, result.ContextHash)

	data, err := os.ReadFile(filepath.Join(tmpDir, agent.SCMContextSubdir, result.ContextHash+".md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Hello ctxloom.", "profile variables must be substituted")
	assert.NotContains(t, string(data), "{{project_name}}", "no literal mustache tags in the injected context")
}

// TestApplyHooks_RegenerateContextUndefinedVariableWarns pins the second
// substitution-warning wiring bug: regenerateContext's per-fragment
// substituteVariables call also used to pass a no-op warnFunc (the
// "// Suppress substitution warnings" call site), so a fragment referencing
// a variable no active profile binds silently rendered empty with no signal
// to the user. This proves the SessionStart-injected context path now warns
// through the same clidiag line as AssembleContext.
//
// Uses a variable name unique to this test ("missing_hook_var") — this file
// shares a test binary/process with context_test.go, and clidiag.WarnOnce's
// dedup is process-global, so reusing another test's exact message would
// make this test's outcome depend on run order.
func TestApplyHooks_RegenerateContextUndefinedVariableWarns(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  greeting:
    content: |
      Hello {{missing_hook_var}}.
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						Fragments: []config.FragmentRef{{Name: "test#fragments/greeting"}},
						// No variables bound — missing_hook_var is undefined.
					},
				},
			},
		}), nil
	}

	var result *ApplyHooksResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend:           "claude-code",
			RegenerateContext: true,
			ConfigLoader:      mockConfigLoader,
			WorkDir:           tmpDir,
		})
		require.NoError(t, err)
	})

	require.NotEmpty(t, result.ContextHash)
	assert.Contains(t, stderr, "ctxloom: warning: undefined variable: {{missing_hook_var}}")
}

// TestApplyHooks_RegenerateContextUndefinedVariableWarningNamesFragment is
// the hooks.go counterpart of
// context_test.go's TestAssembleContext_UndefinedVariableWarningNamesFragment:
// the SessionStart-injected context path assembles two fragments and the
// undefined-variable warning must name the offending one, not the clean one,
// so a mustache mistake surfaced on session start is directly actionable.
func TestApplyHooks_RegenerateContextUndefinedVariableWarningNamesFragment(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  hook-attribution-clean:
    content: |
      Nothing templated here.
  hook-attribution-leaky:
    content: |
      Hello {{hook_attribution_check_variable}}.
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						Fragments: []config.FragmentRef{
							{Name: "test#fragments/hook-attribution-clean"},
							{Name: "test#fragments/hook-attribution-leaky"},
						},
						// No variables bound — hook_attribution_check_variable is undefined.
					},
				},
			},
		}), nil
	}

	var result *ApplyHooksResult
	stderr := captureStderr(t, func() {
		var err error
		result, err = ApplyHooks(context.Background(), ApplyHooksRequest{
			Backend:           "claude-code",
			RegenerateContext: true,
			ConfigLoader:      mockConfigLoader,
			WorkDir:           tmpDir,
		})
		require.NoError(t, err)
	})

	require.NotEmpty(t, result.ContextHash)
	assert.Contains(t, stderr, "ctxloom: warning: undefined variable: {{hook_attribution_check_variable}}")
	assert.Contains(t, stderr, "test#fragments/hook-attribution-leaky",
		"the warning must name the fragment the undefined variable actually came from")
	assert.NotContains(t, stderr, "test#fragments/hook-attribution-clean",
		"the warning must not implicate the fragment that had nothing wrong")
}

// TestApplyHooks_RegenerateContextWithFragments tests regenerateContext with direct fragments.
func TestApplyHooks_RegenerateContextWithFragments(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Create bundle with fragments
	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  my-fragment:
    content: |
      ## My Fragment Content
      This is test content
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						Fragments: []config.FragmentRef{{Name: "test#fragments/my-fragment"}},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.NotEmpty(t, result.ContextHash)
}

// TestApplyHooks_RegenerateContextUnresolvedProfile demonstrates fault tolerance.
//
// FAULT TOLERANCE: When a profile references a non-existent parent profile,
// that profile is skipped rather than failing the entire operation. Other
// profiles continue to be processed normally.
//
// This allows users to keep working even if their config has reference errors.
func TestApplyHooks_RegenerateContextUnresolvedProfile(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Create bundle with a fragment
	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  fallback:
    content: |
      ## Fallback Content
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default", "broken-profile"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						Fragments: []config.FragmentRef{{Name: "test#fragments/fallback"}},
					},
					// This profile is in the default list but references a parent that doesn't exist
					"broken-profile": {
						Parents: []string{"nonexistent-parent"},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	// Should still work, just skip the unresolvable profile
	assert.NotEmpty(t, result.ContextHash)
}

// TestApplyHooks_RegenerateContextMissingFragment demonstrates partial success.
//
// FAULT TOLERANCE: When a profile references a fragment that doesn't exist,
// that fragment is skipped. Other fragments in the same profile are still
// assembled into the context.
//
// This handles cases like:
//   - Remote bundle not yet synced
//   - Fragment renamed but profile not updated
//   - Typo in fragment reference
func TestApplyHooks_RegenerateContextMissingFragment(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Create bundle but fragment doesn't exist
	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  existing:
    content: |
      ## Existing Content
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test.yaml"), []byte(bundleContent), 0644))

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						// Reference both existing and non-existing fragments
						Fragments: []config.FragmentRef{
							{Name: "test#fragments/existing"},
							{Name: "test#fragments/nonexistent"},
						},
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	// Should still work, just skip the missing fragment
	assert.NotEmpty(t, result.ContextHash)
}

// TestApplyHooks_NoWorkDir tests that ApplyHooks works without explicit WorkDir.
func TestApplyHooks_NoWorkDir(t *testing.T) {
	fs := afero.NewMemMapFs()

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{}, nil
	}

	// Call without WorkDir - exercises the gitutil.FindRoot fallback path
	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ConfigLoader: mockConfigLoader,
		// WorkDir not set - will use "." or git root
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
}

// TestApplyHooks_RegenerateContextNoFragments tests regenerateContext when no fragments found.
func TestApplyHooks_RegenerateContextNoFragments(t *testing.T) {
	fs := afero.NewMemMapFs()

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
			AppPaths:     []string{testBaseDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
				Definitions: map[string]config.Profile{
					"default": {
						SelectTags: []string{"nonexistent-tag"}, // No fragments match this select tag
					},
				},
			},
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		FS:                fs,
		ConfigLoader:      mockConfigLoader,
		WorkDir:           "/project",
		BundleLoaderFS:    fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	// No profile-tag-matched fragments were found, but the always-on builtin
	// isolation fragment still injects unconditionally, so a context file is
	// still written and hashed.
	assert.NotEmpty(t, result.ContextHash)
}
