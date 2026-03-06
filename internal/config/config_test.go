package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetDefaultPlugin_ExistingPlugin(t *testing.T) {
	lm := LMConfig{
		Plugins: map[string]PluginConfig{
			"claude-code": {Default: true},
			"gemini":      {Default: false, Model: "gemini-2.0-flash"},
		},
	}

	lm.SetDefaultPlugin("gemini")

	if lm.Plugins["claude-code"].Default {
		t.Error("expected claude-code Default to be false")
	}
	if !lm.Plugins["gemini"].Default {
		t.Error("expected gemini Default to be true")
	}
	if lm.Plugins["gemini"].Model != "gemini-2.0-flash" {
		t.Error("expected gemini Model to be preserved")
	}
}

func TestSetDefaultPlugin_NewPlugin(t *testing.T) {
	lm := LMConfig{
		Plugins: map[string]PluginConfig{
			"claude-code": {Default: true},
		},
	}

	lm.SetDefaultPlugin("aider")

	if lm.Plugins["claude-code"].Default {
		t.Error("expected claude-code Default to be false")
	}
	if !lm.Plugins["aider"].Default {
		t.Error("expected aider Default to be true")
	}
}

func TestSetDefaultPlugin_NilPluginsMap(t *testing.T) {
	lm := LMConfig{}

	lm.SetDefaultPlugin("gemini")

	if lm.Plugins == nil {
		t.Fatal("expected Plugins map to be initialized")
	}
	if !lm.Plugins["gemini"].Default {
		t.Error("expected gemini Default to be true")
	}
}

func TestSetDefaultPlugin_OnlyOneDefault(t *testing.T) {
	lm := LMConfig{
		Plugins: map[string]PluginConfig{
			"claude-code": {Default: true},
			"gemini":      {Default: true},
			"aider":       {Default: false},
		},
	}

	lm.SetDefaultPlugin("aider")

	defaultCount := 0
	for _, cfg := range lm.Plugins {
		if cfg.Default {
			defaultCount++
		}
	}
	if defaultCount != 1 {
		t.Errorf("expected exactly 1 default, got %d", defaultCount)
	}
	if !lm.Plugins["aider"].Default {
		t.Error("expected aider to be the default")
	}
}

func TestResolveProfile_HooksInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: HooksConfig{
				Unified: UnifiedHooks{
					PreTool: []Hook{
						{Command: "./base-hook.sh", Matcher: "Bash"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			Hooks: HooksConfig{
				Unified: UnifiedHooks{
					PostTool: []Hook{
						{Command: "./child-hook.sh", Matcher: "Edit"},
					},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have inherited PreTool from base
	if len(resolved.Hooks.Unified.PreTool) != 1 {
		t.Errorf("expected 1 PreTool hook, got %d", len(resolved.Hooks.Unified.PreTool))
	}
	if resolved.Hooks.Unified.PreTool[0].Command != "./base-hook.sh" {
		t.Errorf("expected base hook command, got %s", resolved.Hooks.Unified.PreTool[0].Command)
	}

	// Should have own PostTool
	if len(resolved.Hooks.Unified.PostTool) != 1 {
		t.Errorf("expected 1 PostTool hook, got %d", len(resolved.Hooks.Unified.PostTool))
	}
}

func TestResolveProfile_HooksDeduplication(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: HooksConfig{
				Unified: UnifiedHooks{
					PreTool: []Hook{
						{Command: "./shared-hook.sh", Matcher: "Bash"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			Hooks: HooksConfig{
				Unified: UnifiedHooks{
					PreTool: []Hook{
						{Command: "./shared-hook.sh", Matcher: "Bash"}, // Duplicate
						{Command: "./unique-hook.sh", Matcher: "Edit"},
					},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have deduplicated PreTool hooks (2 unique, not 3)
	if len(resolved.Hooks.Unified.PreTool) != 2 {
		t.Errorf("expected 2 PreTool hooks after dedup, got %d", len(resolved.Hooks.Unified.PreTool))
	}
}

func TestResolveProfile_BackendHooksInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: HooksConfig{
				Plugins: map[string]BackendHooks{
					"claude-code": {
						"PreToolUse": []Hook{
							{Command: "./base-claude.sh"},
						},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			Hooks: HooksConfig{
				Plugins: map[string]BackendHooks{
					"claude-code": {
						"PostToolUse": []Hook{
							{Command: "./child-claude.sh"},
						},
					},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have inherited claude-code PreToolUse from base
	if len(resolved.Hooks.Plugins["claude-code"]["PreToolUse"]) != 1 {
		t.Errorf("expected 1 PreToolUse hook, got %d", len(resolved.Hooks.Plugins["claude-code"]["PreToolUse"]))
	}

	// Should have own claude-code PostToolUse
	if len(resolved.Hooks.Plugins["claude-code"]["PostToolUse"]) != 1 {
		t.Errorf("expected 1 PostToolUse hook, got %d", len(resolved.Hooks.Plugins["claude-code"]["PostToolUse"]))
	}
}

func TestResolveProfile_MCPInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			MCP: MCPConfig{
				Servers: map[string]MCPServer{
					"base-server": {
						Command: "base-server-cmd",
						Args:    []string{"--base"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: MCPConfig{
				Servers: map[string]MCPServer{
					"child-server": {
						Command: "child-server-cmd",
					},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have inherited base server
	if _, ok := resolved.MCP.Servers["base-server"]; !ok {
		t.Error("expected base-server to be inherited")
	}
	if resolved.MCP.Servers["base-server"].Command != "base-server-cmd" {
		t.Errorf("expected base-server command, got %s", resolved.MCP.Servers["base-server"].Command)
	}

	// Should have own child server
	if _, ok := resolved.MCP.Servers["child-server"]; !ok {
		t.Error("expected child-server to be present")
	}
}

func TestResolveProfile_MCPOverride(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			MCP: MCPConfig{
				Servers: map[string]MCPServer{
					"shared-server": {
						Command: "base-cmd",
						Args:    []string{"--base"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: MCPConfig{
				Servers: map[string]MCPServer{
					"shared-server": {
						Command: "child-cmd",
						Args:    []string{"--child"},
					},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Child should override base for same server name
	if resolved.MCP.Servers["shared-server"].Command != "child-cmd" {
		t.Errorf("expected child to override base server, got %s", resolved.MCP.Servers["shared-server"].Command)
	}
	if len(resolved.MCP.Servers["shared-server"].Args) != 1 || resolved.MCP.Servers["shared-server"].Args[0] != "--child" {
		t.Errorf("expected child args, got %v", resolved.MCP.Servers["shared-server"].Args)
	}
}

func TestResolveProfile_MCPAutoRegisterOverride(t *testing.T) {
	falseVal := false
	trueVal := true

	profiles := map[string]Profile{
		"base": {
			MCP: MCPConfig{
				AutoRegisterSCM: &trueVal,
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: MCPConfig{
				AutoRegisterSCM: &falseVal,
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Child should override base's auto_register_scm
	if resolved.MCP.AutoRegisterSCM == nil {
		t.Fatal("expected AutoRegisterSCM to be set")
	}
	if *resolved.MCP.AutoRegisterSCM != false {
		t.Error("expected child to override AutoRegisterSCM to false")
	}
}

func TestResolveProfile_MCPBackendInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			MCP: MCPConfig{
				Plugins: map[string]map[string]MCPServer{
					"claude-code": {
						"base-claude-server": {
							Command: "base-claude-cmd",
						},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: MCPConfig{
				Plugins: map[string]map[string]MCPServer{
					"claude-code": {
						"child-claude-server": {
							Command: "child-claude-cmd",
						},
					},
					"gemini": {
						"gemini-server": {
							Command: "gemini-cmd",
						},
					},
				},
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have inherited claude-code base server
	if _, ok := resolved.MCP.Plugins["claude-code"]["base-claude-server"]; !ok {
		t.Error("expected base-claude-server to be inherited")
	}

	// Should have own claude-code child server
	if _, ok := resolved.MCP.Plugins["claude-code"]["child-claude-server"]; !ok {
		t.Error("expected child-claude-server to be present")
	}

	// Should have gemini server
	if _, ok := resolved.MCP.Plugins["gemini"]["gemini-server"]; !ok {
		t.Error("expected gemini-server to be present")
	}
}

// =============================================================================
// GetEditorCommand Tests
// =============================================================================

func TestConfig_GetEditorCommand(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		visual      string
		editor      string
		wantCmd     string
		wantArgs    []string
	}{
		{
			name:     "config takes precedence",
			config:   Config{Editor: EditorConfig{Command: "vim", Args: []string{"-n"}}},
			visual:   "code",
			editor:   "nano",
			wantCmd:  "vim",
			wantArgs: []string{"-n"},
		},
		{
			name:     "VISUAL fallback",
			config:   Config{},
			visual:   "code",
			editor:   "nano",
			wantCmd:  "code",
			wantArgs: nil,
		},
		{
			name:     "EDITOR fallback",
			config:   Config{},
			visual:   "",
			editor:   "emacs",
			wantCmd:  "emacs",
			wantArgs: nil,
		},
		{
			name:     "default to nano",
			config:   Config{},
			visual:   "",
			editor:   "",
			wantCmd:  "nano",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore environment
			origVisual := os.Getenv("VISUAL")
			origEditor := os.Getenv("EDITOR")
			defer func() {
				os.Setenv("VISUAL", origVisual)
				os.Setenv("EDITOR", origEditor)
			}()

			os.Setenv("VISUAL", tt.visual)
			os.Setenv("EDITOR", tt.editor)

			cmd, args := tt.config.GetEditorCommand()
			assert.Equal(t, tt.wantCmd, cmd)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// =============================================================================
// MCPConfig Tests
// =============================================================================

func TestMCPConfig_ShouldAutoRegisterSCM(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name   string
		config *MCPConfig
		want   bool
	}{
		{"nil config", nil, true},
		{"nil value defaults true", &MCPConfig{}, true},
		{"explicit true", &MCPConfig{AutoRegisterSCM: &trueVal}, true},
		{"explicit false", &MCPConfig{AutoRegisterSCM: &falseVal}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.config.ShouldAutoRegisterSCM())
		})
	}
}

// =============================================================================
// LMConfig Tests
// =============================================================================

func TestLMConfig_GetDefaultPlugin(t *testing.T) {
	tests := []struct {
		name   string
		config LMConfig
		want   string
	}{
		{
			name:   "no plugins returns claude-code",
			config: LMConfig{},
			want:   "claude-code",
		},
		{
			name: "returns plugin marked default",
			config: LMConfig{
				Plugins: map[string]PluginConfig{
					"claude-code": {Default: false},
					"gemini":      {Default: true},
				},
			},
			want: "gemini",
		},
		{
			name: "no default marked returns claude-code",
			config: LMConfig{
				Plugins: map[string]PluginConfig{
					"aider": {Default: false},
				},
			},
			want: "claude-code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.config.GetDefaultPlugin())
		})
	}
}

func TestLMConfig_GetDefaultModel(t *testing.T) {
	config := LMConfig{
		Plugins: map[string]PluginConfig{
			"claude-code": {Model: "claude-3-opus"},
			"gemini":      {Model: ""},
		},
	}

	assert.Equal(t, "claude-3-opus", config.GetDefaultModel("claude-code"))
	assert.Equal(t, "", config.GetDefaultModel("gemini"))
	assert.Equal(t, "", config.GetDefaultModel("nonexistent"))
}

// =============================================================================
// Defaults Tests
// =============================================================================

func TestDefaults_ShouldUseDistilled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		defaults Defaults
		want     bool
	}{
		{"nil defaults true", Defaults{}, true},
		{"explicit true", Defaults{UseDistilled: &trueVal}, true},
		{"explicit false", Defaults{UseDistilled: &falseVal}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.defaults.ShouldUseDistilled())
		})
	}
}

// =============================================================================
// Config Methods Tests
// =============================================================================

func TestConfig_SourceName(t *testing.T) {
	tests := []struct {
		source ConfigSource
		want   string
	}{
		{SourceProject, "project"},
		{SourceHome, "home"},
		{ConfigSource(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			cfg := &Config{Source: tt.source}
			assert.Equal(t, tt.want, cfg.SourceName())
		})
	}
}

func TestConfig_GetBundleDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create bundles directory
	bundlesDir := filepath.Join(tmpDir, "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	cfg := &Config{SCMPaths: []string{tmpDir}}
	dirs := cfg.GetBundleDirs()

	assert.Len(t, dirs, 1)
	assert.Equal(t, bundlesDir, dirs[0])
}

func TestConfig_GetBundleDirs_NoBundlesDir(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{SCMPaths: []string{tmpDir}}
	dirs := cfg.GetBundleDirs()

	assert.Empty(t, dirs)
}

func TestConfig_GetPluginPaths(t *testing.T) {
	t.Run("uses configured paths", func(t *testing.T) {
		cfg := &Config{
			LM: LMConfig{
				PluginPaths: []string{"/custom/path1", "/custom/path2"},
			},
		}
		paths := cfg.GetPluginPaths()
		assert.Equal(t, []string{"/custom/path1", "/custom/path2"}, paths)
	})

	t.Run("defaults to scm plugins dir", func(t *testing.T) {
		cfg := &Config{
			SCMPaths: []string{"/home/user/.scm"},
		}
		paths := cfg.GetPluginPaths()
		assert.Equal(t, []string{"/home/user/.scm/plugins"}, paths)
	})
}

func TestConfig_GetConfigFilePath(t *testing.T) {
	t.Run("returns path when SCMPaths set", func(t *testing.T) {
		cfg := &Config{SCMPaths: []string{"/path/to/.scm"}}
		path, err := cfg.GetConfigFilePath()
		require.NoError(t, err)
		assert.Equal(t, "/path/to/.scm/config.yaml", path)
	})

	t.Run("errors when no SCMPaths", func(t *testing.T) {
		cfg := &Config{}
		_, err := cfg.GetConfigFilePath()
		assert.Error(t, err)
	})
}

// =============================================================================
// Profile Helper Functions Tests
// =============================================================================

func TestMergeProfiles(t *testing.T) {
	target := map[string]Profile{
		"existing": {Description: "original"},
		"shared":   {Description: "target-shared"},
	}

	source := map[string]Profile{
		"new":    {Description: "from source"},
		"shared": {Description: "source-shared"},
	}

	MergeProfiles(target, source)

	assert.Equal(t, "original", target["existing"].Description)
	assert.Equal(t, "from source", target["new"].Description)
	assert.Equal(t, "source-shared", target["shared"].Description)
}

func TestCollectFragmentsForProfiles(t *testing.T) {
	profiles := map[string]Profile{
		"profile1": {Fragments: []string{"frag1", "frag2"}},
		"profile2": {Fragments: []string{"frag2", "frag3"}},
	}

	t.Run("collects and deduplicates", func(t *testing.T) {
		frags, err := CollectFragmentsForProfiles(profiles, []string{"profile1", "profile2"})
		require.NoError(t, err)
		assert.Equal(t, []string{"frag1", "frag2", "frag3"}, frags)
	})

	t.Run("errors on unknown profile", func(t *testing.T) {
		_, err := CollectFragmentsForProfiles(profiles, []string{"nonexistent"})
		assert.Error(t, err)
	})
}

func TestCollectBundlesForProfiles(t *testing.T) {
	profiles := map[string]Profile{
		"profile1": {Bundles: []string{"bundle1", "bundle2"}},
		"profile2": {Bundles: []string{"bundle2", "bundle3"}},
	}

	bundles, err := CollectBundlesForProfiles(profiles, []string{"profile1", "profile2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bundle1", "bundle2", "bundle3"}, bundles)
}

func TestCollectBundleItemsForProfiles(t *testing.T) {
	profiles := map[string]Profile{
		"profile1": {BundleItems: []string{"bundle#fragments/a", "bundle#fragments/b"}},
		"profile2": {BundleItems: []string{"bundle#fragments/b", "bundle#prompts/c"}},
	}

	items, err := CollectBundleItemsForProfiles(profiles, []string{"profile1", "profile2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bundle#fragments/a", "bundle#fragments/b", "bundle#prompts/c"}, items)
}

func TestFilterProfiles(t *testing.T) {
	all := map[string]Profile{
		"dev":  {Description: "development"},
		"prod": {Description: "production"},
		"test": {Description: "testing"},
	}

	filtered := FilterProfiles(all, []string{"dev", "prod"})

	assert.Len(t, filtered, 2)
	assert.Contains(t, filtered, "dev")
	assert.Contains(t, filtered, "prod")
	assert.NotContains(t, filtered, "test")
}

func TestDedupeStrings(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"empty", []string{}, []string{}},
		{"no duplicates", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"with duplicates", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"all same", []string{"x", "x", "x"}, []string{"x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DedupeStrings(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// ResolveProfile Additional Tests
// =============================================================================

func TestResolveProfile_CircularDependency(t *testing.T) {
	profiles := map[string]Profile{
		"a": {Parents: []string{"b"}},
		"b": {Parents: []string{"a"}},
	}

	_, err := ResolveProfile(profiles, "a")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circular")
}

func TestResolveProfile_UnknownProfile(t *testing.T) {
	profiles := map[string]Profile{}

	_, err := ResolveProfile(profiles, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown profile")
}

func TestResolveProfile_DeepInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"grandparent": {
			Tags:      []string{"gp-tag"},
			Variables: map[string]string{"var1": "gp-value"},
		},
		"parent": {
			Parents:   []string{"grandparent"},
			Tags:      []string{"p-tag"},
			Variables: map[string]string{"var2": "p-value"},
		},
		"child": {
			Parents:   []string{"parent"},
			Tags:      []string{"c-tag"},
			Variables: map[string]string{"var1": "c-value"}, // Override grandparent
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Should have all tags
	assert.Contains(t, resolved.Tags, "gp-tag")
	assert.Contains(t, resolved.Tags, "p-tag")
	assert.Contains(t, resolved.Tags, "c-tag")

	// Child variable should override grandparent
	assert.Equal(t, "c-value", resolved.Variables["var1"])
	assert.Equal(t, "p-value", resolved.Variables["var2"])
}

func TestResolveProfile_DiamondInheritance(t *testing.T) {
	// Diamond: D inherits from B and C, both inherit from A
	profiles := map[string]Profile{
		"a": {Tags: []string{"a-tag"}, Bundles: []string{"bundle-a"}},
		"b": {Parents: []string{"a"}, Tags: []string{"b-tag"}},
		"c": {Parents: []string{"a"}, Tags: []string{"c-tag"}},
		"d": {Parents: []string{"b", "c"}, Tags: []string{"d-tag"}},
	}

	resolved, err := ResolveProfile(profiles, "d")
	require.NoError(t, err)

	// Should have all unique tags (no duplicates from A)
	assert.Contains(t, resolved.Tags, "a-tag")
	assert.Contains(t, resolved.Tags, "b-tag")
	assert.Contains(t, resolved.Tags, "c-tag")
	assert.Contains(t, resolved.Tags, "d-tag")

	// Bundle from A should appear only once
	bundleCount := 0
	for _, b := range resolved.Bundles {
		if b == "bundle-a" {
			bundleCount++
		}
	}
	assert.Equal(t, 1, bundleCount)
}

// =============================================================================
// LoadFromDir Tests
// =============================================================================

func TestLoadFromDir(t *testing.T) {
	t.Run("loads valid config", func(t *testing.T) {
		tmpDir := t.TempDir()
		configContent := `
lm:
  plugins:
    claude-code:
      default: true
defaults:
  profile: dev
`
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configContent), 0644))

		cfg, err := LoadFromDir(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, "dev", cfg.Defaults.Profile)
		assert.True(t, cfg.LM.Plugins["claude-code"].Default)
	})

	t.Run("returns empty config for missing file", func(t *testing.T) {
		tmpDir := t.TempDir()

		cfg, err := LoadFromDir(tmpDir)
		require.NoError(t, err)
		assert.NotNil(t, cfg.Profiles)
	})

	t.Run("errors on invalid yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte("invalid: ["), 0644))

		_, err := LoadFromDir(tmpDir)
		assert.Error(t, err)
	})
}

// =============================================================================
// Config Save Tests
// =============================================================================

func TestConfig_Save(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		SCMPaths: []string{tmpDir},
		LM: LMConfig{
			Plugins: map[string]PluginConfig{
				"claude-code": {Default: true},
			},
		},
		Defaults: Defaults{
			Profile: "dev",
		},
		Profiles: map[string]Profile{
			"dev": {Description: "development"},
		},
	}

	err := cfg.Save()
	require.NoError(t, err)

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "claude-code")
	assert.Contains(t, string(data), "profile: dev")
}

func TestConfig_Save_NoSCMPaths(t *testing.T) {
	cfg := &Config{}
	err := cfg.Save()
	assert.Error(t, err)
}

// =============================================================================
// Load and LoadOption Tests
// =============================================================================

func TestWithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	opt := WithFS(fs)

	opts := &loadOptions{}
	opt(opts)

	assert.Equal(t, fs, opts.fs)
}

func TestWithSCMDir(t *testing.T) {
	opt := WithSCMDir("/custom/.scm")

	opts := &loadOptions{}
	opt(opts)

	assert.Equal(t, "/custom/.scm", opts.scmDir)
}

func TestLoad_WithOptions(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create .scm directory structure
	scmDir := "/project/.scm"
	require.NoError(t, fs.MkdirAll(scmDir, 0755))

	// Create a valid config file
	configContent := `
lm:
  plugins:
    claude-code:
      default: true
defaults:
  profile: test
`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(scmDir, "config.yaml"), []byte(configContent), 0644))

	cfg, err := Load(WithFS(fs), WithSCMDir(scmDir))
	require.NoError(t, err)

	assert.Equal(t, "test", cfg.Defaults.Profile)
	assert.Equal(t, []string{scmDir}, cfg.SCMPaths)
	assert.Equal(t, scmDir, cfg.SCMDir)
	assert.Equal(t, SourceProject, cfg.Source)
}

func TestLoad_NoConfigFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"
	require.NoError(t, fs.MkdirAll(scmDir, 0755))

	// No config.yaml file - should still work
	cfg, err := Load(WithFS(fs), WithSCMDir(scmDir))
	require.NoError(t, err)

	assert.NotNil(t, cfg.Profiles)
	assert.NotNil(t, cfg.LM.Plugins)
}

func TestLoadConfigFile_Errors(t *testing.T) {
	t.Run("file not found is not error", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		cfg := &Config{}
		// Missing file should be OK - config is optional
		err := loadConfigFile(cfg, "/nonexistent/config.yaml", nil, fs)
		assert.NoError(t, err)
	})

	t.Run("invalid yaml", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte("invalid: ["), 0644))

		cfg := &Config{}
		err := loadConfigFile(cfg, "/config.yaml", nil, fs)
		assert.Error(t, err)
	})
}

// =============================================================================
// SetFS Tests
// =============================================================================

func TestConfig_SetFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &Config{}

	cfg.SetFS(fs)

	assert.Equal(t, fs, cfg.fs)
}

func TestConfig_getFS_UsesSetFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &Config{fs: fs}

	result := cfg.getFS()

	assert.Equal(t, fs, result)
}

// =============================================================================
// GetDefaultProfiles Tests
// =============================================================================

func TestConfig_GetDefaultProfiles(t *testing.T) {
	t.Run("returns defaults.profile", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		scmDir := "/project/.scm"
		require.NoError(t, fs.MkdirAll(filepath.Join(scmDir, "profiles"), 0755))

		cfg := &Config{
			Defaults: Defaults{Profile: "dev"},
			Profiles: map[string]Profile{},
			SCMPaths: []string{scmDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		assert.Contains(t, defaults, "dev")
	})

	t.Run("includes profiles with default true", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		scmDir := "/project/.scm"
		require.NoError(t, fs.MkdirAll(filepath.Join(scmDir, "profiles"), 0755))

		cfg := &Config{
			Profiles: map[string]Profile{
				"prod": {Default: true},
				"dev":  {Default: false},
			},
			SCMPaths: []string{scmDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		assert.Contains(t, defaults, "prod")
		assert.NotContains(t, defaults, "dev")
	})

	t.Run("no duplicates", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		scmDir := "/project/.scm"
		require.NoError(t, fs.MkdirAll(filepath.Join(scmDir, "profiles"), 0755))

		cfg := &Config{
			Defaults: Defaults{Profile: "prod"},
			Profiles: map[string]Profile{
				"prod": {Default: true}, // Same profile also marked default
			},
			SCMPaths: []string{scmDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		count := 0
		for _, d := range defaults {
			if d == "prod" {
				count++
			}
		}
		assert.Equal(t, 1, count)
	})

	t.Run("returns nil when no defaults", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		scmDir := "/project/.scm"
		require.NoError(t, fs.MkdirAll(filepath.Join(scmDir, "profiles"), 0755))

		cfg := &Config{
			Profiles: map[string]Profile{},
			SCMPaths: []string{scmDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		assert.Nil(t, defaults)
	})
}

// =============================================================================
// GetProfileLoader Tests
// =============================================================================

func TestConfig_GetProfileLoader(t *testing.T) {
	cfg := &Config{
		SCMPaths: []string{"/project/.scm"},
	}

	loader := cfg.GetProfileLoader()

	assert.NotNil(t, loader)
}

// =============================================================================
// ResolveProfile - addFragment and addBundleItem Coverage
// =============================================================================

func TestResolveProfile_FragmentsAndBundleItems(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Fragments:   []string{"frag1", "frag2"},
			BundleItems: []string{"bundle#item1"},
		},
		"child": {
			Parents:     []string{"base"},
			Fragments:   []string{"frag2", "frag3"}, // frag2 duplicate
			BundleItems: []string{"bundle#item1", "bundle#item2"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Fragments should be deduplicated
	assert.Equal(t, []string{"frag1", "frag2", "frag3"}, resolved.Fragments)

	// BundleItems should be deduplicated
	assert.Equal(t, []string{"bundle#item1", "bundle#item2"}, resolved.BundleItems)
}

// =============================================================================
// mergeHooks Coverage - PreShell and PostFileEdit
// =============================================================================

func TestResolveProfile_AllUnifiedHooks(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: HooksConfig{
				Unified: UnifiedHooks{
					PreShell:     []Hook{{Command: "./pre-shell.sh"}},
					PostFileEdit: []Hook{{Command: "./post-edit.sh"}},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	assert.Len(t, resolved.Hooks.Unified.PreShell, 1)
	assert.Len(t, resolved.Hooks.Unified.PostFileEdit, 1)
}

// =============================================================================
// extractMCPFromBundle Tests
// =============================================================================

func TestExtractMCPFromBundle(t *testing.T) {
	bundle := &bundles.Bundle{
		MCP: map[string]bundles.BundleMCP{
			"test-server": {
				Command: "test-cmd",
				Args:    []string{"--arg1"},
				Env:     map[string]string{"KEY": "value"},
				Note:    "Test server",
			},
		},
	}

	result := extractMCPFromBundle(bundle, "my-bundle")

	assert.Len(t, result, 1)
	assert.Equal(t, "test-cmd", result["test-server"].Command)
	assert.Equal(t, []string{"--arg1"}, result["test-server"].Args)
	assert.Equal(t, "value", result["test-server"].Env["KEY"])
	assert.Equal(t, "Test server", result["test-server"].Note)
	assert.Equal(t, "bundle:my-bundle", result["test-server"].SCM)
}

// =============================================================================
// CollectBundlesForProfiles Error Case
// =============================================================================

func TestCollectBundlesForProfiles_UnknownProfile(t *testing.T) {
	profiles := map[string]Profile{
		"known": {Bundles: []string{"bundle1"}},
	}

	_, err := CollectBundlesForProfiles(profiles, []string{"unknown"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown profile")
}

// =============================================================================
// CollectBundleItemsForProfiles Error Case
// =============================================================================

func TestCollectBundleItemsForProfiles_UnknownProfile(t *testing.T) {
	profiles := map[string]Profile{
		"known": {BundleItems: []string{"item1"}},
	}

	_, err := CollectBundleItemsForProfiles(profiles, []string{"unknown"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown profile")
}

// =============================================================================
// resolveProfileRecursive Depth Limit
// =============================================================================

func TestResolveProfile_DepthLimit(t *testing.T) {
	// Create a very deep inheritance chain
	profiles := make(map[string]Profile)
	for i := 0; i < 100; i++ {
		name := "profile" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		parent := ""
		if i > 0 {
			prev := i - 1
			parent = "profile" + string(rune('a'+prev%26)) + string(rune('0'+prev/26))
		}
		if parent != "" {
			profiles[name] = Profile{Parents: []string{parent}}
		} else {
			profiles[name] = Profile{}
		}
	}

	// Get the last profile name
	lastName := "profile" + string(rune('a'+99%26)) + string(rune('0'+99/26))

	_, err := ResolveProfile(profiles, lastName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "depth exceeds maximum")
}

// =============================================================================
// ResolveBundleMCPServers Tests
// =============================================================================

func TestConfig_ResolveBundleMCPServers_NoDefaultProfile(t *testing.T) {
	cfg := &Config{
		Defaults: Defaults{},
		SCMPaths: []string{"/project/.scm"},
	}

	result := cfg.ResolveBundleMCPServers()
	assert.Empty(t, result)
}

func TestConfig_ResolveBundleMCPServers_NoSCMPaths(t *testing.T) {
	cfg := &Config{
		Defaults: Defaults{Profile: "test"},
		SCMPaths: []string{},
	}

	result := cfg.ResolveBundleMCPServers()
	assert.Empty(t, result)
}

func TestConfig_ResolveBundleMCPServers_ProfileNotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"
	require.NoError(t, fs.MkdirAll(filepath.Join(scmDir, "profiles"), 0755))

	cfg := &Config{
		Defaults: Defaults{Profile: "nonexistent"},
		SCMPaths: []string{scmDir},
		fs:       fs,
	}

	result := cfg.ResolveBundleMCPServers()
	assert.Empty(t, result)
}

// =============================================================================
// loadMCPFromBundleRef Tests
// =============================================================================

func TestLoadMCPFromBundleRef_LocalBundle(t *testing.T) {
	tmpDir := t.TempDir()
	bundlesDir := filepath.Join(tmpDir, "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Create a test bundle
	bundleContent := `
name: test-bundle
version: "1.0"
mcp:
  test-server:
    command: test-cmd
    args: ["--arg"]
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "test-bundle.yaml"), []byte(bundleContent), 0644))

	loader := bundles.NewLoader([]string{bundlesDir}, false)
	result := loadMCPFromBundleRef("test-bundle", tmpDir, loader)

	assert.Len(t, result, 1)
	assert.Equal(t, "test-cmd", result["test-server"].Command)
}

func TestLoadMCPFromBundleRef_InvalidRef(t *testing.T) {
	tmpDir := t.TempDir()
	loader := bundles.NewLoader([]string{tmpDir}, false)

	// Invalid bundle reference
	result := loadMCPFromBundleRef("nonexistent-bundle", tmpDir, loader)
	assert.Empty(t, result)
}

// =============================================================================
// Save Additional Coverage
// =============================================================================

func TestConfig_Save_WithMCP(t *testing.T) {
	tmpDir := t.TempDir()
	trueVal := true

	cfg := &Config{
		SCMPaths: []string{tmpDir},
		LM: LMConfig{
			Plugins: map[string]PluginConfig{},
		},
		MCP: MCPConfig{
			AutoRegisterSCM: &trueVal,
			Servers: map[string]MCPServer{
				"test": {Command: "test-cmd"},
			},
		},
	}

	err := cfg.Save()
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "mcp")
	assert.Contains(t, string(data), "test-cmd")
}

func TestConfig_Save_PreservesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Write existing config with custom fields
	existingContent := `
custom_field: preserved
lm:
  plugins: {}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(existingContent), 0644))

	cfg := &Config{
		SCMPaths: []string{tmpDir},
		LM: LMConfig{
			Plugins: map[string]PluginConfig{
				"claude-code": {Default: true},
			},
		},
	}

	err := cfg.Save()
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)
	// Should preserve the custom field
	assert.Contains(t, string(data), "custom_field")
}

// =============================================================================
// GetDefaultProfiles Additional Coverage
// =============================================================================

func TestConfig_GetDefaultProfiles_FromDirectoryProfile(t *testing.T) {
	// Test when directory-based profiles have defaults
	tmpDir := t.TempDir()
	profilesDir := filepath.Join(tmpDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	// Create a profile file with default: true
	profileContent := `
default: true
description: A default profile
`
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dir-profile.yaml"), []byte(profileContent), 0644))

	cfg := &Config{
		Profiles: map[string]Profile{},
		SCMPaths: []string{tmpDir},
	}

	defaults := cfg.GetDefaultProfiles()
	assert.Contains(t, defaults, "dir-profile")
}

// =============================================================================
// Load Schema Validation Error
// =============================================================================

func TestLoad_SchemaValidationError(t *testing.T) {
	fs := afero.NewMemMapFs()
	scmDir := "/project/.scm"
	require.NoError(t, fs.MkdirAll(scmDir, 0755))

	// Create config that fails schema validation (using wrong type)
	// Note: This depends on having a strict schema - may need adjustment
	configContent := `
lm:
  plugins: "should be a map not string"
`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(scmDir, "config.yaml"), []byte(configContent), 0644))

	_, err := Load(WithFS(fs), WithSCMDir(scmDir))
	// May fail on schema validation or YAML unmarshaling
	assert.Error(t, err)
}

// =============================================================================
// mergeHooks Complete Coverage (SessionEnd)
// =============================================================================

func TestResolveProfile_SessionEndHooks(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: HooksConfig{
				Unified: UnifiedHooks{
					SessionEnd: []Hook{{Command: "./session-end.sh"}},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	assert.Len(t, resolved.Hooks.Unified.SessionEnd, 1)
	assert.Equal(t, "./session-end.sh", resolved.Hooks.Unified.SessionEnd[0].Command)
}
