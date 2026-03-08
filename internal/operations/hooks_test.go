// Package operations tests for ApplyHooks verify the hook application system.
//
// Hooks are the glue between SCM configuration and LLM backend settings files.
// ApplyHooks transforms SCM's unified hook config into backend-specific formats
// (e.g., .claude/settings.json, .gemini/settings.json) and regenerates context.
//
// # What ApplyHooks Does
//
//  1. Reads SCM config (hooks, MCP servers, profiles)
//  2. Writes backend-specific settings files with hooks
//  3. Writes .mcp.json with MCP server configs
//  4. Optionally regenerates context file from active profiles
//  5. Creates symlink to SCM binary for backend access
//
// # Test Injection Patterns
//
// Tests inject dependencies to avoid real filesystem operations:
//   - ConfigLoader: Function returning mock config instead of reading disk
//   - FS: afero virtual filesystem for settings file writes
//   - SkipSymlink: Disables symlink creation (MemMapFs doesn't support symlinks)
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

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/lm/backends"
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

func TestApplyHooksRequest_Gemini(t *testing.T) {
	req := ApplyHooksRequest{
		Backend: "gemini",
	}

	assert.Equal(t, "gemini", req.Backend)
}

func TestApplyHooksResult_Fields(t *testing.T) {
	result := ApplyHooksResult{
		Status:      "applied",
		Backends:    []string{"claude-code", "gemini"},
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
	validBackends := []string{"all", "claude-code", "gemini", ""}

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
		FS:       fs,
		ExecPath: "/usr/bin/scm",
	}

	assert.NotNil(t, req.FS)
	assert.Equal(t, "/usr/bin/scm", req.ExecPath)
}

// ==========================================================================
// WriteSettings tests
// ==========================================================================
//
// WriteSettings is the lower-level function that creates backend-specific
// settings files. ApplyHooks calls this internally.

// TestWriteSettings_ClaudeCode verifies Claude Code settings file creation.
// Claude Code reads .claude/settings.json for hooks, customization, etc.
func TestWriteSettings_ClaudeCode(t *testing.T) {
	fs := afero.NewMemMapFs()

	hooks := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	err := backends.WriteSettings("claude-code", hooks, nil, nil, "/project",
		backends.WithSettingsFS(fs))
	require.NoError(t, err)

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

// TestWriteSettings_Gemini tests writing Gemini settings with FS injection.
func TestWriteSettings_Gemini(t *testing.T) {
	fs := afero.NewMemMapFs()

	hooks := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	err := backends.WriteSettings("gemini", hooks, nil, nil, "/project",
		backends.WithSettingsFS(fs))
	require.NoError(t, err)

	// Verify settings file was created
	exists, err := afero.Exists(fs, "/project/.gemini/settings.json")
	require.NoError(t, err)
	assert.True(t, exists, ".gemini/settings.json should be created")

	// Read and verify content contains hooks
	content, err := afero.ReadFile(fs, "/project/.gemini/settings.json")
	require.NoError(t, err)
	assert.Contains(t, string(content), "hooks")
}

// TestWriteSettings_UnsupportedBackend tests that unsupported backends are no-ops.
func TestWriteSettings_UnsupportedBackend(t *testing.T) {
	fs := afero.NewMemMapFs()

	err := backends.WriteSettings("unknown-backend", nil, nil, nil, "/project",
		backends.WithSettingsFS(fs))

	// Should not error, just be a no-op
	assert.NoError(t, err)
}

// TestWriteSettings_PreservesExistingSettings verifies that user customizations survive.
//
// NON-OBVIOUS: When SCM writes hooks to settings.json, it MERGES with existing
// content rather than overwriting. User's custom settings (like theme preferences)
// are preserved. Only SCM-managed sections (hooks, MCP servers) are updated.
//
// This is important because users may have manually configured settings that
// they don't want SCM to destroy on each hook application.
func TestWriteSettings_PreservesExistingSettings(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create existing settings with custom user config
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	existingContent := `{
  "customSetting": "userValue",
  "anotherSetting": 123
}`
	require.NoError(t, afero.WriteFile(fs, "/project/.claude/settings.json", []byte(existingContent), 0644))

	hooks := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{
				{Command: "echo hello", Type: "command"},
			},
		},
	}

	err := backends.WriteSettings("claude-code", hooks, nil, nil, "/project",
		backends.WithSettingsFS(fs))
	require.NoError(t, err)

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
// The file is stored in .scm/context/{hash}.md where hash is content-based.
func TestWriteContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()

	fragments := []*backends.Fragment{
		{Name: "frag1", Content: "First fragment content"},
		{Name: "frag2", Content: "Second fragment content"},
	}

	hash, err := backends.WriteContextFile("/project", fragments,
		backends.WithContextFS(fs))
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Verify context file was created
	contextPath := "/project/.scm/context/" + hash + ".md"
	exists, err := afero.Exists(fs, contextPath)
	require.NoError(t, err)
	assert.True(t, exists, "context file should be created")

	// Read and verify content
	content, err := afero.ReadFile(fs, contextPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "First fragment content")
	assert.Contains(t, string(content), "Second fragment content")
}

// TestWriteContextFile_Empty tests that empty fragments produce empty hash.
func TestWriteContextFile_Empty(t *testing.T) {
	fs := afero.NewMemMapFs()

	hash, err := backends.WriteContextFile("/project", nil,
		backends.WithContextFS(fs))
	require.NoError(t, err)
	assert.Empty(t, hash)

	// Also test with empty content fragments
	fragments := []*backends.Fragment{
		{Name: "empty", Content: ""},
	}
	hash, err = backends.WriteContextFile("/project", fragments,
		backends.WithContextFS(fs))
	require.NoError(t, err)
	assert.Empty(t, hash)
}

// TestEnsureSCMSymlink tests symlink creation with FS injection.
func TestEnsureSCMSymlink_CreatesBinDir(t *testing.T) {
	// Use real OS filesystem for symlink support
	tmpDir := t.TempDir()

	// Create a dummy executable to link to
	execPath := tmpDir + "/scm-binary"
	require.NoError(t, afero.WriteFile(afero.NewOsFs(), execPath, []byte("#!/bin/sh\necho scm"), 0755))

	symlinkPath, err := backends.EnsureSCMSymlink(tmpDir,
		backends.WithExecPath(execPath))
	require.NoError(t, err)
	assert.Contains(t, symlinkPath, ".scm/bin/scm")

	// Verify directory was created
	info, err := afero.NewOsFs().Stat(tmpDir + "/.scm/bin")
	require.NoError(t, err)
	assert.True(t, info.IsDir())
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
		return &config.Config{
			Hooks: config.HooksConfig{
				Unified: config.UnifiedHooks{
					SessionStart: []config.Hook{
						{Command: "echo test", Type: "command"},
					},
				},
			},
		}, nil
	}

	// Create a dummy executable path for symlink
	execPath := "/usr/bin/scm"

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ExecPath:     execPath,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
		SkipSymlink:  true, // MemMapFs doesn't support symlinks
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, result.Backends, "claude-code")
	assert.NotContains(t, result.Backends, "gemini")

	// Verify Claude Code settings file was created
	exists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists)

	// Verify Gemini settings file was NOT created
	exists, err = afero.Exists(fs, "/project/.gemini/settings.json")
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestApplyHooks_GeminiOnly tests applying hooks to Gemini backend only.
func TestApplyHooks_GeminiOnly(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{
			Hooks: config.HooksConfig{
				Unified: config.UnifiedHooks{
					SessionStart: []config.Hook{
						{Command: "echo test", Type: "command"},
					},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "gemini",
		FS:           fs,
		ExecPath:     "/usr/bin/scm",
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
		SkipSymlink:  true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, result.Backends, "gemini")
	assert.NotContains(t, result.Backends, "claude-code")

	// Verify Gemini settings file was created
	exists, err := afero.Exists(fs, "/project/.gemini/settings.json")
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestApplyHooks_AllBackends tests applying hooks to all backends.
func TestApplyHooks_AllBackends(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{
			Hooks: config.HooksConfig{
				Unified: config.UnifiedHooks{
					SessionStart: []config.Hook{
						{Command: "echo hello", Type: "command"},
					},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "all",
		FS:           fs,
		ExecPath:     "/usr/bin/scm",
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
		SkipSymlink:  true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.Len(t, result.Backends, 2)
	assert.Contains(t, result.Backends, "claude-code")
	assert.Contains(t, result.Backends, "gemini")

	// Verify both settings files were created
	exists, err := afero.Exists(fs, "/project/.claude/settings.json")
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = afero.Exists(fs, "/project/.gemini/settings.json")
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

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "", // empty should default to "all"
		FS:           fs,
		ExecPath:     "/usr/bin/scm",
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
		SkipSymlink:  true,
	})

	require.NoError(t, err)
	assert.Len(t, result.Backends, 2)
}

// TestApplyHooks_ConfigLoadError tests error handling when config load fails.
func TestApplyHooks_ConfigLoadError(t *testing.T) {
	fs := afero.NewMemMapFs()

	mockConfigLoader := func() (*config.Config, error) {
		return nil, fmt.Errorf("config file not found")
	}

	_, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ExecPath:     "/usr/bin/scm",
		ConfigLoader: mockConfigLoader,
		WorkDir:      "/project",
		SkipSymlink:  true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}

// TestApplyHooks_WithMCPServers tests that MCP servers are written correctly.
func TestApplyHooks_WithMCPServers(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{
			MCP: config.MCPConfig{
				Servers: map[string]config.MCPServer{
					"test-server": {
						Command: "test-cmd",
						Args:    []string{"arg1", "arg2"},
					},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ExecPath:     "/usr/bin/scm",
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
		SkipSymlink:  true,
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
		return &config.Config{
			// No profiles or fragments - regenerateContext should return empty
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		FS:                fs,
		ExecPath:          "/usr/bin/scm",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
		SkipSymlink:       true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.Empty(t, result.ContextHash) // No context hash since no fragments
}

// TestApplyHooks_RegenerateContextWithTags tests regenerateContext with profile tags.
func TestApplyHooks_RegenerateContextWithTags(t *testing.T) {
	tmpDir := t.TempDir()
	scmDir := filepath.Join(tmpDir, ".scm")
	bundlesDir := filepath.Join(scmDir, "bundles")
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
		return &config.Config{
			SCMPaths: []string{scmDir},
			Profiles: map[string]config.Profile{
				"default": {
					Default: true,
					Tags:    []string{"security"},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ExecPath:          "/usr/bin/scm",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
		SkipSymlink:       true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.NotEmpty(t, result.ContextHash) // Should have context hash since fragments were found
}

// TestApplyHooks_RegenerateContextWithFragments tests regenerateContext with direct fragments.
func TestApplyHooks_RegenerateContextWithFragments(t *testing.T) {
	tmpDir := t.TempDir()
	scmDir := filepath.Join(tmpDir, ".scm")
	bundlesDir := filepath.Join(scmDir, "bundles")
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
		return &config.Config{
			SCMPaths: []string{scmDir},
			Profiles: map[string]config.Profile{
				"default": {
					Default:   true,
					Fragments: []string{"test#fragments/my-fragment"},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ExecPath:          "/usr/bin/scm",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
		SkipSymlink:       true,
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
	scmDir := filepath.Join(tmpDir, ".scm")
	bundlesDir := filepath.Join(scmDir, "bundles")
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
		return &config.Config{
			SCMPaths: []string{scmDir},
			Profiles: map[string]config.Profile{
				"default": {
					Default:   true,
					Fragments: []string{"test#fragments/fallback"},
				},
				// This profile will be in the default list but references a parent that doesn't exist
				"broken-profile": {
					Default: true,
					Parents: []string{"nonexistent-parent"},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ExecPath:          "/usr/bin/scm",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
		SkipSymlink:       true,
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
	scmDir := filepath.Join(tmpDir, ".scm")
	bundlesDir := filepath.Join(scmDir, "bundles")
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
		return &config.Config{
			SCMPaths: []string{scmDir},
			Profiles: map[string]config.Profile{
				"default": {
					Default: true,
					// Reference both existing and non-existing fragments
					Fragments: []string{"test#fragments/existing", "test#fragments/nonexistent"},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ExecPath:          "/usr/bin/scm",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
		SkipSymlink:       true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	// Should still work, just skip the missing fragment
	assert.NotEmpty(t, result.ContextHash)
}

// TestApplyHooks_CreatesSymlink tests that the SCM symlink directory is created.
func TestApplyHooks_CreatesSymlink(t *testing.T) {
	// Use real filesystem for symlink support
	tmpDir := t.TempDir()

	// Create a dummy executable to link to
	execPath := tmpDir + "/scm-binary"
	require.NoError(t, afero.WriteFile(afero.NewOsFs(), execPath, []byte("#!/bin/sh\necho scm"), 0755))

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{}, nil
	}

	_, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		ExecPath:     execPath,
		ConfigLoader: mockConfigLoader,
		WorkDir:      tmpDir,
		// No SkipSymlink - we want to test symlink creation
	})

	require.NoError(t, err)

	// Verify .scm/bin directory was created
	exists, err := afero.DirExists(afero.NewOsFs(), tmpDir+"/.scm/bin")
	require.NoError(t, err)
	assert.True(t, exists)
}

// TestApplyHooks_NoWorkDir tests that ApplyHooks works without explicit WorkDir.
func TestApplyHooks_NoWorkDir(t *testing.T) {
	fs := afero.NewMemMapFs()

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{}, nil
	}

	// Call without WorkDir - exercises the gitutil.FindRoot fallback path
	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:      "claude-code",
		FS:           fs,
		ExecPath:     "/usr/bin/scm",
		ConfigLoader: mockConfigLoader,
		// WorkDir not set - will use "." or git root
		SkipSymlink: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
}

// TestApplyHooks_RegenerateContextNoFragments tests regenerateContext when no fragments found.
func TestApplyHooks_RegenerateContextNoFragments(t *testing.T) {
	fs := afero.NewMemMapFs()
	tmpDir := "/project"

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{
			SCMPaths: []string{tmpDir + "/.scm"},
			Profiles: map[string]config.Profile{
				"default": {
					Default: true,
					Tags:    []string{"nonexistent-tag"}, // No fragments match this tag
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		FS:                fs,
		ExecPath:          "/usr/bin/scm",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
		SkipSymlink:       true,
		BundleLoaderFS:    fs,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	// No context hash since no fragments were found
	assert.Empty(t, result.ContextHash)
}
