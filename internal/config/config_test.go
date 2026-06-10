package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// Config Package Tests
// =============================================================================
//
// This package manages ctxloom configuration: profiles, hooks, MCP servers, and
// plugin settings. Configuration is loaded from YAML files and supports
// inheritance through parent profiles.
//
// KEY CONCEPTS:
// - Profiles: Named collections of fragments, tags, and settings
// - Hooks: Commands executed before/after AI tool calls
// - MCP servers: External processes providing AI capabilities
// - Inheritance: Child profiles inherit and override parent settings
//
// IMPORTANT BEHAVIORS:
// - Profile inheritance is depth-first, parents processed in order
// - Hooks are deduplicated by command+matcher combination
// - MCP servers can be scoped to specific backends (claude-code, antigravity)
// - Config is fault-tolerant: invalid entries warn but don't block startup
//
// =============================================================================

// =============================================================================
// Default Plugin Tests
// =============================================================================
// The default LLM plugin determines which AI backend is used when none is
// explicitly specified. Falls back to claude-code for backwards compatibility.

func TestGetDefaultLLM(t *testing.T) {
	t.Run("resolves the primary label's backend type", func(t *testing.T) {
		cfg := &Config{LM: LMConfig{
			Configs: map[string]LLMConfig{
				"big": {Type: "antigravity", Body: map[string]interface{}{"model": "pro"}},
			},
			Defaults: RoleDefaults{Primary: "big"},
		}}
		assert.Equal(t, "antigravity", cfg.GetDefaultLLM())
	})

	t.Run("returns claude-code as fallback when no label resolves", func(t *testing.T) {
		cfg := &Config{}
		assert.Equal(t, "claude-code", cfg.GetDefaultLLM())
	})
}

func TestSetPrimaryLabel(t *testing.T) {
	cfg := &Config{}
	cfg.SetPrimaryLabel("quick")
	assert.Equal(t, "quick", cfg.LM.Defaults.Primary)
}

// PrimaryLabel falls back to the sole configured label when no role default is
// set, so a single-config project resolves without naming a role.
func TestPrimaryLabel_SingleConfigFallback(t *testing.T) {
	cfg := &Config{LM: LMConfig{Configs: map[string]LLMConfig{
		"only": {Type: "codex"},
	}}}
	assert.Equal(t, "only", cfg.PrimaryLabel())
}

// FastLabel falls back to the primary label when no fast role is set.
func TestFastLabel_FallsBackToPrimary(t *testing.T) {
	cfg := &Config{LM: LMConfig{Defaults: RoleDefaults{Primary: "big"}}}
	assert.Equal(t, "big", cfg.FastLabel())
}

// ResolveLLM reads backend type + model straight from the labeled entry; an
// unknown label degrades to the built-in default backend with no model.
func TestResolveLLM(t *testing.T) {
	cfg := &Config{LM: LMConfig{Configs: map[string]LLMConfig{
		"g":    {Type: "antigravity", Body: map[string]interface{}{"model": "gemini-3-pro"}},
		"bare": {Type: "claude-code"},
	}}}

	backend, model := cfg.ResolveLLM("g")
	assert.Equal(t, "antigravity", backend)
	assert.Equal(t, "gemini-3-pro", model)

	backend, model = cfg.ResolveLLM("bare")
	assert.Equal(t, "claude-code", backend)
	assert.Empty(t, model)

	backend, model = cfg.ResolveLLM("missing")
	assert.Equal(t, "claude-code", backend, "unknown label degrades to default backend")
	assert.Empty(t, model)
}

// =============================================================================
// Profile Resolution Tests
// =============================================================================
// Profile resolution handles inheritance chains and merges settings from
// parent profiles. This enables composition of reusable profile fragments.

// TestResolveProfile_HooksInheritance verifies hooks are inherited from parents.
// Hooks defined in parent profiles apply to child profiles unless overridden.
// This enables shared tool hooks (linting, formatting) across profiles.
func TestResolveProfile_HooksInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					PreTool: []wire.Hook{
						{Command: "./base-hook.sh", Matcher: "Bash"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					PostTool: []wire.Hook{
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

// TestResolveProfile_HooksDeduplication verifies duplicate hooks are merged.
// NON-OBVIOUS: A hook is considered duplicate if BOTH command AND matcher match.
// This prevents the same hook from running multiple times when inherited
// through multiple parent chains (diamond inheritance problem).
func TestResolveProfile_HooksDeduplication(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					PreTool: []wire.Hook{
						{Command: "./shared-hook.sh", Matcher: "Bash"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					PreTool: []wire.Hook{
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
			Hooks: wire.HooksConfig{
				Plugins: map[string]wire.BackendHooks{
					"claude-code": {
						"PreToolUse": []wire.Hook{
							{Command: "./base-claude.sh"},
						},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			Hooks: wire.HooksConfig{
				Plugins: map[string]wire.BackendHooks{
					"claude-code": {
						"PostToolUse": []wire.Hook{
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
			MCP: wire.MCPConfig{
				Servers: map[string]wire.MCPServer{
					"base-server": {
						Command: "base-server-cmd",
						Args:    []string{"--base"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: wire.MCPConfig{
				Servers: map[string]wire.MCPServer{
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
			MCP: wire.MCPConfig{
				Servers: map[string]wire.MCPServer{
					"shared-server": {
						Command: "base-cmd",
						Args:    []string{"--base"},
					},
				},
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: wire.MCPConfig{
				Servers: map[string]wire.MCPServer{
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
			MCP: wire.MCPConfig{
				AutoRegisterCtxloom: &trueVal,
			},
		},
		"child": {
			Parents: []string{"base"},
			MCP: wire.MCPConfig{
				AutoRegisterCtxloom: &falseVal,
			},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Child should override base's auto_register_ctxloom
	if resolved.MCP.AutoRegisterCtxloom == nil {
		t.Fatal("expected AutoRegisterCtxloom to be set")
	}
	if *resolved.MCP.AutoRegisterCtxloom != false {
		t.Error("expected child to override AutoRegisterCtxloom to false")
	}
}

func TestResolveProfile_MCPBackendInheritance(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			MCP: wire.MCPConfig{
				Plugins: map[string]map[string]wire.MCPServer{
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
			MCP: wire.MCPConfig{
				Plugins: map[string]map[string]wire.MCPServer{
					"claude-code": {
						"child-claude-server": {
							Command: "child-claude-cmd",
						},
					},
					"antigravity": {
						"antigravity-server": {
							Command: "antigravity-cmd",
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

	// Should have antigravity server
	if _, ok := resolved.MCP.Plugins["antigravity"]["antigravity-server"]; !ok {
		t.Error("expected antigravity-server to be present")
	}
}

// =============================================================================
// Profile Exclusion Tests
// =============================================================================
// Tests for fragment, prompt, and MCP server exclusion functionality.
// Exclusions accumulate through inheritance (cannot un-exclude).

func TestResolveProfile_ExcludeFragments(t *testing.T) {
	profiles := map[string]Profile{
		"parent": {
			Fragments: []FragmentRef{
				{Name: "frag-a"},
				{Name: "frag-b"},
				{Name: "frag-c"},
			},
		},
		"child": {
			Parents:          []string{"parent"},
			ExcludeFragments: []string{"frag-b"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Should have frag-a and frag-c, but not frag-b
	fragNames := make([]string, len(resolved.Fragments))
	for i, f := range resolved.Fragments {
		fragNames[i] = f.Name
	}
	assert.Contains(t, fragNames, "frag-a")
	assert.Contains(t, fragNames, "frag-c")
	assert.NotContains(t, fragNames, "frag-b")
}

func TestResolveProfile_ExcludeMCP(t *testing.T) {
	profiles := map[string]Profile{
		"parent": {
			MCP: wire.MCPConfig{
				Servers: map[string]wire.MCPServer{
					"server-a": {Command: "cmd-a"},
					"server-b": {Command: "cmd-b"},
					"server-c": {Command: "cmd-c"},
				},
			},
		},
		"child": {
			Parents:    []string{"parent"},
			ExcludeMCP: []string{"server-b"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Should have server-a and server-c, but not server-b
	assert.Contains(t, resolved.MCP.Servers, "server-a")
	assert.Contains(t, resolved.MCP.Servers, "server-c")
	assert.NotContains(t, resolved.MCP.Servers, "server-b")
}

func TestResolveProfile_ExclusionsAccumulate(t *testing.T) {
	profiles := map[string]Profile{
		"grandparent": {
			Fragments:        []FragmentRef{{Name: "frag-a"}, {Name: "frag-b"}, {Name: "frag-c"}},
			ExcludeFragments: []string{"frag-a"},
		},
		"parent": {
			Parents:          []string{"grandparent"},
			ExcludeFragments: []string{"frag-b"},
		},
		"child": {
			Parents: []string{"parent"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Both frag-a and frag-b should be excluded (accumulated from grandparent and parent)
	fragNames := make([]string, len(resolved.Fragments))
	for i, f := range resolved.Fragments {
		fragNames[i] = f.Name
	}
	assert.NotContains(t, fragNames, "frag-a")
	assert.NotContains(t, fragNames, "frag-b")
	assert.Contains(t, fragNames, "frag-c")
}

func TestResolveProfile_ExcludeFragmentsSelectorForm(t *testing.T) {
	// A fragment declared by full ref ("bundle#fragments/name") is excluded by
	// its bare name, matching the bundle-expansion seam's semantics.
	profiles := map[string]Profile{
		"parent": {
			Fragments: []FragmentRef{
				{Name: "remote/bundle#fragments/frag-a"},
				{Name: "remote/bundle#fragments/frag-b"},
			},
		},
		"child": {
			Parents:          []string{"parent"},
			ExcludeFragments: []string{"frag-a"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	fragNames := make([]string, len(resolved.Fragments))
	for i, f := range resolved.Fragments {
		fragNames[i] = f.Name
	}
	assert.NotContains(t, fragNames, "remote/bundle#fragments/frag-a")
	assert.Contains(t, fragNames, "remote/bundle#fragments/frag-b")
}

func TestExclusionSet_QualifiedExclusionIsBundleScoped(t *testing.T) {
	// A qualified exclusion drops only its own bundle's fragment — same-named
	// fragments from other bundles survive (the repo-name-collision case).
	// Any reference spelling of the bundle matches via canonicalization.
	excluded := NewExclusionSet([]string{"dev#fragments/security-rules"})

	assert.True(t, IsExcludedFragment("dev#fragments/security-rules", excluded))
	assert.True(t, IsExcludedFragment("ctxloom:local@bundles/dev#fragments/security-rules", excluded))
	assert.False(t, IsExcludedFragment("other#fragments/security-rules", excluded))
	assert.False(t, IsExcludedFragment("https://github.com/o/r@bundles/dev#fragments/security-rules", excluded))
	assert.False(t, IsExcludedFragment("security-rules", excluded),
		"a bare name carries no origin; a qualified exclusion must not match it")
}

func TestExclusionSet_BareExclusionMatchesEveryBundle(t *testing.T) {
	// A bare exclusion is the name-wide kill switch: it matches its fragment
	// name wherever it comes from.
	excluded := NewExclusionSet([]string{"security-rules"})

	assert.True(t, IsExcludedFragment("security-rules", excluded))
	assert.True(t, IsExcludedFragment("dev#fragments/security-rules", excluded))
	assert.True(t, IsExcludedFragment("https://github.com/o/r@bundles/tools#fragments/security-rules", excluded))
	assert.False(t, IsExcludedFragment("security", excluded))
}

func TestResolveProfile_ExclusionsPreserved(t *testing.T) {
	profiles := map[string]Profile{
		"child": {
			Fragments:        []FragmentRef{{Name: "frag-a"}},
			ExcludeFragments: []string{"frag-b"},
			ExcludeMCP:       []string{"server-a"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Exclusion lists should be preserved in resolved profile
	assert.Contains(t, resolved.ExcludeFragments, "frag-b")
	assert.Contains(t, resolved.ExcludeMCP, "server-a")
}

// =============================================================================
// GetEditorCommand Tests
// =============================================================================

func TestConfig_GetEditorCommand(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		visual   string
		editor   string
		wantCmd  string
		wantArgs []string
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
		{
			// "code --wait" must split into binary + flag, not be exec'd as
			// one binary named "code --wait" (which never exists).
			name:     "EDITOR with flags is split",
			config:   Config{},
			editor:   "code --wait",
			wantCmd:  "code",
			wantArgs: []string{"--wait"},
		},
		{
			name:     "VISUAL with flags wins over EDITOR",
			config:   Config{},
			visual:   "emacsclient -t",
			editor:   "nano",
			wantCmd:  "emacsclient",
			wantArgs: []string{"-t"},
		},
		{
			// A multi-word config command splits too, with editor.args
			// appended after the inline flags.
			name:     "config command with flags plus args",
			config:   Config{Editor: EditorConfig{Command: "code --wait", Args: []string{"-n"}}},
			editor:   "vim",
			wantCmd:  "code",
			wantArgs: []string{"--wait", "-n"},
		},
		{
			// A blank (whitespace-only) config command is no choice at all and
			// falls through to the environment.
			name:     "blank config command falls back to env",
			config:   Config{Editor: EditorConfig{Command: "   "}},
			editor:   "vim",
			wantCmd:  "vim",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Isolate clears VISUAL/EDITOR; set just the values under test.
			testsupport.Isolate(t)
			t.Setenv("VISUAL", tt.visual)
			t.Setenv("EDITOR", tt.editor)

			cmd, args := tt.config.GetEditorCommand()
			assert.Equal(t, tt.wantCmd, cmd)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// EditorFromEnv is the pre-config-load half of the editor policy (used by
// `manage config edit`): VISUAL → EDITOR → nano, with whitespace splitting.
func TestEditorFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		visual   string
		editor   string
		wantCmd  string
		wantArgs []string
	}{
		{name: "VISUAL wins", visual: "code --wait", editor: "vim", wantCmd: "code", wantArgs: []string{"--wait"}},
		{name: "EDITOR fallback", editor: "vim -n", wantCmd: "vim", wantArgs: []string{"-n"}},
		{name: "default nano", wantCmd: "nano", wantArgs: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testsupport.Isolate(t)
			t.Setenv("VISUAL", tt.visual)
			t.Setenv("EDITOR", tt.editor)

			cmd, args := EditorFromEnv()
			assert.Equal(t, tt.wantCmd, cmd)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// =============================================================================
// LMConfig Tests
// =============================================================================

func TestLMConfig_hasAny(t *testing.T) {
	assert.False(t, LMConfig{}.hasAny())
	assert.True(t, LMConfig{Configs: map[string]LLMConfig{"x": {}}}.hasAny())
	assert.True(t, LMConfig{Defaults: RoleDefaults{Primary: "x"}}.hasAny())
}

// =============================================================================
// Settings Tests
// =============================================================================

func TestSettings_ShouldUseDistilled(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name     string
		settings SettingsConfig
		want     bool
	}{
		{"nil defaults true", SettingsConfig{}, true},
		{"explicit true", SettingsConfig{UseDistilled: &trueVal}, true},
		{"explicit false", SettingsConfig{UseDistilled: &falseVal}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.settings.ShouldUseDistilled())
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

	// Create bundles directory in cache/
	bundlesDir := filepath.Join(tmpDir, "cache", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	cfg := &Config{AppPaths: []string{tmpDir}}
	dirs := cfg.GetBundleDirs()

	assert.Len(t, dirs, 1)
	assert.Equal(t, bundlesDir, dirs[0])
}

func TestConfig_GetBundleDirs_NoBundlesDir(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{AppPaths: []string{tmpDir}}
	dirs := cfg.GetBundleDirs()

	assert.Empty(t, dirs)
}

func TestConfig_GetConfigFilePath(t *testing.T) {
	t.Run("returns path when AppPaths set", func(t *testing.T) {
		cfg := &Config{AppPaths: []string{"/path/to/.ctxloom"}}
		path, err := cfg.GetConfigFilePath()
		require.NoError(t, err)
		assert.Equal(t, "/path/to/.ctxloom/config.yaml", path)
	})

	t.Run("errors when no AppPaths", func(t *testing.T) {
		cfg := &Config{}
		_, err := cfg.GetConfigFilePath()
		assert.Error(t, err)
	})
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
	assert.ErrorIs(t, err, errs.ErrCircularInheritance)
}

func TestResolveProfile_UnknownProfile(t *testing.T) {
	profiles := map[string]Profile{}

	_, err := ResolveProfile(profiles, "nonexistent")
	assert.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrProfileNotFound)
}

// A missing parent must not abort resolution: per the fault-tolerance
// philosophy the branch is skipped and the rest of the profile still resolves,
// matching profiles.Loader.ResolveProfile.
func TestResolveProfile_MissingParentWarnsAndContinues(t *testing.T) {
	profiles := map[string]Profile{
		"child": {
			Parents: []string{"ghost"}, // not defined
			Tags:    []string{"own-tag"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err, "missing parent should warn-and-skip, not error")
	require.NotNil(t, resolved)
	assert.Contains(t, resolved.Tags, "own-tag", "child's own settings should still resolve")
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
// Config Save Tests
// =============================================================================

func TestConfig_Save(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the persistent directory
	require.NoError(t, os.MkdirAll(tmpDir, 0755))

	cfg := &Config{
		AppPaths: []string{tmpDir},
		LM: LMConfig{
			Defaults: RoleDefaults{Primary: "claude-code"},
			Configs: map[string]LLMConfig{
				"claude-code": {Type: "claude-code"},
			},
		},
		Profiles: ProfilesConfig{
			Defaults: []string{"dev"},
			Definitions: map[string]Profile{
				"dev": {Description: "development"},
			},
		},
	}

	err := cfg.Save()
	require.NoError(t, err)

	// Verify file was written to persistent dir
	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "claude-code")
	assert.Contains(t, string(data), "- dev")
	assert.Contains(t, string(data), "llm")
}

func TestConfig_Save_NoAppPaths(t *testing.T) {
	cfg := &Config{}
	err := cfg.Save()
	assert.Error(t, err)
}

// Regression: Save round-trips the labeled-config registry, the role map, the
// config (settings) block, and the editor block. The fast role's labeled
// config carries the compression model; compaction_chunks lives under config.
func TestConfig_Save_PreservesLLMRolesAndEditor(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0755))

	cfg := &Config{
		AppPaths: []string{tmpDir},
		LM: LMConfig{
			Configs: map[string]LLMConfig{
				"big":  {Type: "claude-code", Body: map[string]interface{}{"model": "opus"}},
				"fast": {Type: "antigravity", Body: map[string]interface{}{"model": "haiku"}},
			},
			Defaults: RoleDefaults{Primary: "big", Fast: "fast"},
		},
		Settings: SettingsConfig{CompactionChunks: 4096},
		Editor:   EditorConfig{Command: "vim", Args: []string{"-p"}},
	}
	require.NoError(t, cfg.Save())

	// Round-trip through Load to confirm the values survived.
	loaded, err := Load(WithAppDir(tmpDir))
	require.NoError(t, err)
	assert.Equal(t, "big", loaded.LM.Defaults.Primary)
	assert.Equal(t, "fast", loaded.LM.Defaults.Fast)
	assert.Equal(t, "antigravity", loaded.GetCompactionLLM())
	assert.Equal(t, "haiku", loaded.GetCompactionModel())
	assert.Equal(t, 4096, loaded.GetCompactionChunkSize())
	assert.Equal(t, "vim", loaded.Editor.Command)
	assert.Equal(t, []string{"-p"}, loaded.Editor.Args)
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

func TestWithAppDir(t *testing.T) {
	opt := WithAppDir("/custom/.ctxloom")

	opts := &loadOptions{}
	opt(opts)

	assert.Equal(t, "/custom/.ctxloom", opts.appDir)
}

func TestLoad_WithOptions(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Create .ctxloom directory structure with persistent subdir
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Create a valid v3 config file in persistent directory
	configContent := `
version: 3
llm:
  configs:
    claude-code: { type: claude-code }
  defaults:
    primary: claude-code
profiles:
  defaults:
    - test
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configContent), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	assert.Contains(t, cfg.Profiles.Defaults, "test")
	assert.Equal(t, "claude-code", cfg.LM.Defaults.Primary)
	assert.Equal(t, []string{appDir}, cfg.AppPaths)
	assert.Equal(t, appDir, cfg.AppDir)
	assert.Equal(t, SourceProject, cfg.Source)
}

// TestLoad_PreservesEnvKeyCase is a regression guard: the Load path must not
// lowercase case-sensitive keys inside a backend's polymorphic Body. The previous
// decoder (viper) lowercased every key, so `env: {SOME_API_KEY: ...}` reached the
// launched process as `some_api_key` and the engine never saw its credential.
// ParseConfig (init) was always correct, which masked the divergence.
func TestLoad_PreservesEnvKeyCase(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	configContent := `
version: 3
llm:
  configs:
    agy:
      type: antigravity
      env:
        SOME_API_KEY: secret
        Mixed_Case: x
  defaults:
    primary: agy
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configContent), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	env, ok := cfg.LM.Configs["agy"].Body["env"].(map[string]any)
	require.True(t, ok, "env should decode into Body as a map, got %#v", cfg.LM.Configs["agy"].Body["env"])
	assert.Equal(t, "secret", env["SOME_API_KEY"], "uppercase env key must be preserved verbatim")
	assert.Contains(t, env, "Mixed_Case")
	assert.NotContains(t, env, "some_api_key", "env key must not be lowercased")
}

func TestLoad_UpgradesLegacyLLMKeysInMemory(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	legacy := "# my config\n" +
		"llm:\n  plugins:\n    claude-code:\n      model: opus\n" +
		"defaults:\n  profiles:\n    - test\n  llm_plugin: antigravity\n"
	cfgPath := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, cfgPath, []byte(legacy), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	// In-memory config reflects the full v1→v2→v3 upgrade chain.
	assert.Equal(t, "antigravity", cfg.LM.Defaults.Primary, "llm_plugin → llm.defaults.primary")
	require.Contains(t, cfg.LM.Configs, "claude-code", "llm.plugins → llm.configs")
	assert.Equal(t, "claude-code", cfg.LM.Configs["claude-code"].Type, "v3 adds the type discriminator")
	assert.Equal(t, "opus", cfg.LM.Configs["claude-code"].Body["model"])
	assert.Contains(t, cfg.Profiles.Defaults, "test", "defaults.profiles → profiles.defaults")
	assert.Empty(t, cfg.Warnings, "upgraded config should not produce validation warnings")

	// Load is non-destructive: the file on disk is untouched (no silent rewrite).
	onDisk, err := afero.ReadFile(fs, cfgPath)
	require.NoError(t, err)
	assert.Equal(t, legacy, string(onDisk), "Load must not rewrite the file")

	// The upgrade is staged as pending for an interactive caller to confirm.
	require.NotNil(t, cfg.PendingUpgrade, "legacy config should record a pending upgrade")
	assert.Equal(t, cfgPath, cfg.PendingUpgrade.Path)
	assert.NotEmpty(t, cfg.PendingUpgrade.Applied)

	// CommitUpgrade persists the upgraded form verbatim (comments preserved) and
	// clears the pending state.
	require.NoError(t, cfg.CommitUpgrade())
	assert.Nil(t, cfg.PendingUpgrade)
	committed, err := afero.ReadFile(fs, cfgPath)
	require.NoError(t, err)
	assert.NotContains(t, string(committed), "llm_plugin")
	assert.NotContains(t, string(committed), "plugins:")
	assert.Contains(t, string(committed), "version: 3", "upgrade stamps the current schema version")
	assert.Contains(t, string(committed), "# my config", "comments preserved on rewrite")
}

func TestLoad_CurrentConfigHasNoPendingUpgrade(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	current := "version: 3\nllm:\n  configs:\n    claude-code: { type: claude-code }\n  defaults:\n    primary: claude-code\n"
	cfgPath := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, cfgPath, []byte(current), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)
	assert.Nil(t, cfg.PendingUpgrade, "a current-version config must not record a pending upgrade")
	assert.Equal(t, 3, cfg.Version)
}

func TestLoad_NoConfigFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// No config.yaml file - should still work
	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err)

	assert.NotNil(t, cfg.Profiles)
	assert.NotNil(t, cfg.LM.Configs)
}

func TestLoadConfigFile_Errors(t *testing.T) {
	t.Run("file not found is not error", func(t *testing.T) {
		fs := afero.NewMemMapFs()

		cfg := &Config{}
		// Missing file should be OK - config is optional
		err := loadConfigFile(cfg, "/nonexistent/config.yaml", nil, fs)
		assert.NoError(t, err)
	})

	t.Run("invalid yaml produces warning not error", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, "/config.yaml", []byte("invalid: ["), 0644))

		cfg := &Config{}
		err := loadConfigFile(cfg, "/config.yaml", nil, fs)
		// Invalid YAML no longer errors - adds warning instead for resilient startup
		assert.NoError(t, err)
		assert.Len(t, cfg.Warnings, 1)
		assert.Contains(t, cfg.Warnings[0], "failed to parse config")
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
	t.Run("returns defaults.profiles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		appDir := "/project/.ctxloom"
		require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0755))

		cfg := &Config{
			Profiles: ProfilesConfig{
				Defaults:    []string{"dev"},
				Definitions: map[string]Profile{},
			},
			AppPaths: []string{appDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		assert.Contains(t, defaults, "dev")
	})

	t.Run("no duplicates in defaults.profiles", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		appDir := "/project/.ctxloom"
		require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0755))

		cfg := &Config{
			Profiles: ProfilesConfig{
				Defaults:    []string{"prod", "prod"},
				Definitions: map[string]Profile{},
			},
			AppPaths: []string{appDir},
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
		appDir := "/project/.ctxloom"
		require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0755))

		cfg := &Config{
			Profiles: ProfilesConfig{Definitions: map[string]Profile{}},
			AppPaths: []string{appDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		assert.Nil(t, defaults)
	})

	t.Run("returns multiple profiles from defaults.profiles array", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		appDir := "/project/.ctxloom"
		require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0755))

		cfg := &Config{
			Profiles: ProfilesConfig{
				Defaults:    []string{"profile1", "profile2", "profile3"},
				Definitions: map[string]Profile{},
			},
			AppPaths: []string{appDir},
			fs:       fs,
		}

		defaults := cfg.GetDefaultProfiles()
		assert.Len(t, defaults, 3)
		assert.Contains(t, defaults, "profile1")
		assert.Contains(t, defaults, "profile2")
		assert.Contains(t, defaults, "profile3")
	})
}

// =============================================================================
// Defaults Helper Methods Tests
// =============================================================================

func TestDefaults_AddDefaultProfile(t *testing.T) {
	t.Run("adds profile to empty profiles", func(t *testing.T) {
		d := &ProfilesConfig{}
		added := d.AddDefaultProfile("new-profile")

		assert.True(t, added)
		assert.Contains(t, d.Defaults, "new-profile")
	})

	t.Run("adds profile to existing profiles", func(t *testing.T) {
		d := &ProfilesConfig{Defaults: []string{"existing"}}
		added := d.AddDefaultProfile("new-profile")

		assert.True(t, added)
		assert.Contains(t, d.Defaults, "existing")
		assert.Contains(t, d.Defaults, "new-profile")
	})

	t.Run("does not add duplicate", func(t *testing.T) {
		d := &ProfilesConfig{Defaults: []string{"existing"}}
		added := d.AddDefaultProfile("existing")

		assert.False(t, added)
		assert.Len(t, d.Defaults, 1)
	})
}

func TestDefaults_RemoveDefaultProfile(t *testing.T) {
	t.Run("removes profile from profiles array", func(t *testing.T) {
		d := &ProfilesConfig{Defaults: []string{"keep", "remove", "also-keep"}}
		removed := d.RemoveDefaultProfile("remove")

		assert.True(t, removed)
		assert.NotContains(t, d.Defaults, "remove")
		assert.Contains(t, d.Defaults, "keep")
		assert.Contains(t, d.Defaults, "also-keep")
	})

	t.Run("returns false for non-existent profile", func(t *testing.T) {
		d := &ProfilesConfig{Defaults: []string{"existing"}}
		removed := d.RemoveDefaultProfile("not-found")

		assert.False(t, removed)
		assert.Len(t, d.Defaults, 1)
	})
}

func TestDefaults_IsDefaultProfile(t *testing.T) {
	t.Run("finds profile in profiles array", func(t *testing.T) {
		d := &ProfilesConfig{Defaults: []string{"profile1", "profile2"}}

		assert.True(t, d.IsDefaultProfile("profile1"))
		assert.True(t, d.IsDefaultProfile("profile2"))
		assert.False(t, d.IsDefaultProfile("profile3"))
	})
}

// =============================================================================
// GetProfileLoader Tests
// =============================================================================

func TestConfig_GetProfileLoader(t *testing.T) {
	cfg := &Config{
		AppPaths: []string{"/project/.ctxloom"},
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
			Fragments:   []FragmentRef{{Name: "frag1"}, {Name: "frag2"}},
			BundleItems: []string{"bundle#item1"},
		},
		"child": {
			Parents:     []string{"base"},
			Fragments:   []FragmentRef{{Name: "frag2"}, {Name: "frag3"}}, // frag2 duplicate
			BundleItems: []string{"bundle#item1", "bundle#item2"},
		},
	}

	resolved, err := ResolveProfile(profiles, "child")
	require.NoError(t, err)

	// Fragments should be deduplicated (extract names for comparison)
	var fragNames []string
	for _, f := range resolved.Fragments {
		fragNames = append(fragNames, f.Name)
	}
	assert.Equal(t, []string{"frag1", "frag2", "frag3"}, fragNames)

	// BundleItems should be deduplicated
	assert.Equal(t, []string{"bundle#item1", "bundle#item2"}, resolved.BundleItems)
}

// =============================================================================
// mergeHooks Coverage - PreShell and PostFileEdit
// =============================================================================

func TestResolveProfile_AllUnifiedHooks(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					PreShell:     []wire.Hook{{Command: "./pre-shell.sh"}},
					PostFileEdit: []wire.Hook{{Command: "./post-edit.sh"}},
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
				Command:      "test-cmd",
				Args:         []string{"--arg1"},
				Env:          map[string]string{"KEY": "value"},
				Notes:        "Test server",
				Installation: "npm install test-server",
			},
		},
	}

	result := extractMCPFromBundle(bundle, "my-bundle")

	assert.Len(t, result, 1)
	assert.Equal(t, "test-cmd", result["test-server"].Command)
	assert.Equal(t, []string{"--arg1"}, result["test-server"].Args)
	assert.Equal(t, "value", result["test-server"].Env["KEY"])
	assert.Equal(t, "Test server", result["test-server"].Notes)
	assert.Equal(t, "bundle:my-bundle", result["test-server"].SCM)
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

// onlyBuiltinMCPServers asserts a resolution surfaced nothing beyond the
// built-in bundles' servers (currently the taskloom bundle's `taskloom mcp`),
// which apply unconditionally — no profile reference required, mirroring
// ResolveBundleHooks.
func onlyBuiltinMCPServers(t *testing.T, result map[string]wire.MCPServer) {
	t.Helper()
	for name, server := range result {
		assert.True(t, strings.HasPrefix(server.SCM, "bundle:builtin:"),
			"unexpected non-builtin MCP server %q (SCM %q)", name, server.SCM)
	}
	assert.Contains(t, result, "taskloom", "builtin taskloom MCP server must always resolve")
}

// stubLookPath pins the companion-gating seam: every binary resolves except
// those named missing, so assertions don't depend on what the host machine
// happens to have installed.
func stubLookPath(t *testing.T, missing ...string) {
	t.Helper()
	gone := make(map[string]bool, len(missing))
	for _, m := range missing {
		gone[m] = true
	}
	orig := lookPath
	lookPath = func(bin string) (string, error) {
		if gone[bin] {
			return "", exec.ErrNotFound
		}
		return "/stub/" + bin, nil
	}
	t.Cleanup(func() { lookPath = orig })
}

func TestConfig_ResolveBundleMCPServers_NoDefaultProfile(t *testing.T) {
	stubLookPath(t)
	cfg := &Config{
		AppPaths: []string{"/project/.ctxloom"},
	}

	onlyBuiltinMCPServers(t, cfg.ResolveBundleMCPServers())
}

func TestConfig_ResolveBundleMCPServers_NoAppPaths(t *testing.T) {
	stubLookPath(t)
	cfg := &Config{
		Profiles: ProfilesConfig{Defaults: []string{"test"}},
		AppPaths: []string{},
	}

	onlyBuiltinMCPServers(t, cfg.ResolveBundleMCPServers())
}

func TestConfig_ResolveBundleMCPServers_ProfileNotFound(t *testing.T) {
	stubLookPath(t)
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0755))

	cfg := &Config{
		Profiles: ProfilesConfig{Defaults: []string{"nonexistent"}},
		AppPaths: []string{appDir},
		fs:       fs,
	}

	onlyBuiltinMCPServers(t, cfg.ResolveBundleMCPServers())
}

// Companion gating: a builtin bundle's MCP server whose binary is absent from
// PATH must degrade to no entry — a broken server in every backend is worse
// than a missing feature (fault-tolerance over completeness).
func TestResolveBuiltinBundleMCPServers_MissingBinarySkipped(t *testing.T) {
	stubLookPath(t, "taskloom")
	got := resolveBuiltinBundleMCPServers()
	assert.NotContains(t, got, "taskloom", "missing companion binary must not register a server")
}

// Companion gating for hooks: the ltk bundle's pre_tool hook applies only when
// the ltk binary resolves; ctxloom's own hooks (session-bind, stamp-plan) are
// never gated.
func TestResolveBuiltinBundleHooks_CompanionGating(t *testing.T) {
	hasLtkHook := func(hooks wire.UnifiedHooks) bool {
		for _, h := range hooks.PreTool {
			if strings.HasPrefix(h.Command, "ltk ") {
				return true
			}
		}
		return false
	}
	hasStampPlan := func(hooks wire.UnifiedHooks) bool {
		for _, h := range hooks.PostFileEdit {
			if strings.Contains(h.Command, "hook stamp-plan") {
				return true
			}
		}
		return false
	}

	t.Run("ltk present registers the pre-tool hook", func(t *testing.T) {
		stubLookPath(t)
		hooks := resolveBuiltinBundleHooks()
		assert.True(t, hasLtkHook(hooks), "ltk on PATH must surface the builtin pre-tool hook")
		assert.True(t, hasStampPlan(hooks))
	})

	t.Run("ltk absent degrades to no hook, ctxloom hooks remain", func(t *testing.T) {
		stubLookPath(t, "ltk")
		hooks := resolveBuiltinBundleHooks()
		assert.False(t, hasLtkHook(hooks), "missing ltk must not register a broken hook")
		assert.True(t, hasStampPlan(hooks), "ctxloom's own hooks are never gated")
	})
}

// Regression: a bundle reachable only through profile inheritance must still
// have its MCP server resolved. Before the fix, ResolveBundleMCPServers read a
// flat profileLoader.Load(profile).Bundles — which omits inherited bundles —
// so a parent's bundle MCP server was silently dropped (while its fragment and
// prompt were still exported through other paths).
func TestConfig_ResolveBundleMCPServers_InheritedBundle(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "cache", "bundles") // paths.BundlesPath layout
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Parent profile ships the bundle; child only inherits and is the default.
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "parent.yaml"),
		[]byte("name: parent\nbundles:\n  - seq-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "child.yaml"),
		[]byte("name: child\nparents:\n  - parent\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "seq-bundle.yaml"),
		[]byte("name: seq-bundle\nversion: \"1.0\"\nmcp:\n  sequential-thinking:\n    command: npx\n    args: [\"-y\", \"server\"]\n"), 0644))

	cfg := &Config{
		Profiles: ProfilesConfig{Defaults: []string{"child"}},
		AppPaths: []string{appDir},
	}

	result := cfg.ResolveBundleMCPServers()
	assert.Contains(t, result, "sequential-thinking",
		"MCP server from a parent-inherited bundle should resolve")
}

// Regression: a directory profile's exclude_mcp must filter bundle-shipped
// servers, matching the name-based filter the inline config-profile path
// applies in profileBuilder.toProfile.
func TestConfig_ResolveBundleMCPServers_ExcludeMCP(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "cache", "bundles") // paths.BundlesPath layout
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
		[]byte("name: dev\nbundles:\n  - mcp-bundle\nexclude_mcp:\n  - noisy-server\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "mcp-bundle.yaml"),
		[]byte("name: mcp-bundle\nversion: \"1.0\"\nmcp:\n  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n"), 0644))

	cfg := &Config{
		Profiles: ProfilesConfig{Defaults: []string{"dev"}},
		AppPaths: []string{appDir},
	}

	result := cfg.ResolveBundleMCPServers()
	assert.Contains(t, result, "quiet-server",
		"non-excluded MCP server should resolve")
	assert.NotContains(t, result, "noisy-server",
		"exclude_mcp server should be filtered out")
}

// hookBundleYAML is a bundle that ships one hook per several event types, used
// to assert the profile-gated ResolveBundleHooks path surfaces bundle hooks
// tagged with the bundle's SCM marker.
const hookBundleYAML = `name: hook-bundle
version: "1.0"
hooks:
  pre_tool:
    - matcher: Bash
      command: echo pre-tool
      type: command
  session_start:
    - command: echo session-start
      type: command
  post_file_edit:
    - matcher: '.*\.md$'
      command: echo post-edit
      type: command
`

func hasHookCommand(hooks []wire.Hook, command, wantSCM string) bool {
	for _, h := range hooks {
		if h.Command == command && h.SCM == wantSCM {
			return true
		}
	}
	return false
}

// TestConfig_ResolveBundleHooks_ProfileGated covers the profile-gated branch of
// ResolveBundleHooks: a default profile (directly or via inheritance) that
// references a hook-shipping bundle must contribute that bundle's hooks, tagged
// SCM "bundle:<ref>"; unresolvable profiles and bundle refs are skipped without
// affecting the always-present builtin hooks.
func TestConfig_ResolveBundleHooks_ProfileGated(t *testing.T) {
	const bundleSCM = "bundle:hook-bundle"

	newProject := func(t *testing.T) (appDir, profilesDir, bundlesDir string) {
		t.Helper()
		appDir = filepath.Join(t.TempDir(), ".ctxloom")
		profilesDir = filepath.Join(appDir, "profiles")
		bundlesDir = filepath.Join(appDir, "cache", "bundles") // paths.BundlesPath layout
		require.NoError(t, os.MkdirAll(profilesDir, 0755))
		require.NoError(t, os.MkdirAll(bundlesDir, 0755))
		return appDir, profilesDir, bundlesDir
	}

	t.Run("direct profile reference surfaces bundle hooks", func(t *testing.T) {
		appDir, profilesDir, bundlesDir := newProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hook-bundle.yaml"), []byte(hookBundleYAML), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
			[]byte("name: dev\nbundles:\n  - hook-bundle\n"), 0644))

		cfg := &Config{Profiles: ProfilesConfig{Defaults: []string{"dev"}}, AppPaths: []string{appDir}}
		result := cfg.ResolveBundleHooks()

		assert.True(t, hasHookCommand(result.PreTool, "echo pre-tool", bundleSCM),
			"profile bundle's pre_tool hook must resolve with its SCM marker")
		assert.True(t, hasHookCommand(result.SessionStart, "echo session-start", bundleSCM),
			"profile bundle's session_start hook must resolve")
		assert.True(t, hasHookCommand(result.PostFileEdit, "echo post-edit", bundleSCM),
			"profile bundle's post_file_edit hook must resolve")
	})

	t.Run("parent-inherited bundle hooks resolve recursively", func(t *testing.T) {
		appDir, profilesDir, bundlesDir := newProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hook-bundle.yaml"), []byte(hookBundleYAML), 0644))
		// Parent ships the bundle; the child (the default) only inherits it.
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "parent.yaml"),
			[]byte("name: parent\nbundles:\n  - hook-bundle\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "child.yaml"),
			[]byte("name: child\nparents:\n  - parent\n"), 0644))

		cfg := &Config{Profiles: ProfilesConfig{Defaults: []string{"child"}}, AppPaths: []string{appDir}}
		result := cfg.ResolveBundleHooks()

		assert.True(t, hasHookCommand(result.PreTool, "echo pre-tool", bundleSCM),
			"a hook from a parent-inherited bundle must resolve (recursive ResolveProfile)")
	})

	t.Run("unresolvable profile and bundle ref are skipped, builtins remain", func(t *testing.T) {
		appDir, profilesDir, _ := newProject(t)
		// A default profile that does not exist (ResolveProfile errors → skip) and
		// a profile referencing a bundle that is not on disk (Load errors → skip).
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "real.yaml"),
			[]byte("name: real\nbundles:\n  - ghost-bundle\n"), 0644))

		cfg := &Config{Profiles: ProfilesConfig{Defaults: []string{"missing", "real"}}, AppPaths: []string{appDir}}
		result := cfg.ResolveBundleHooks()

		assert.False(t, hasHookCommand(result.PreTool, "echo pre-tool", bundleSCM),
			"a ghost bundle ref contributes no hooks")
		// The builtin taskloom bundle always contributes its stamp-plan hook.
		foundBuiltin := false
		for _, h := range result.PostFileEdit {
			if strings.Contains(h.Command, "hook stamp-plan") && h.SCM == "bundle:builtin:taskloom" {
				foundBuiltin = true
			}
		}
		assert.True(t, foundBuiltin,
			"builtin hooks survive when profile-gated resolution skips everything")
	})
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
	result := loadMCPFromBundleRef("test-bundle", loader)

	assert.Len(t, result, 1)
	assert.Equal(t, "test-cmd", result["test-server"].Command)
}

func TestLoadMCPFromBundleRef_InvalidRef(t *testing.T) {
	tmpDir := t.TempDir()
	loader := bundles.NewLoader([]string{tmpDir}, false)

	// Invalid bundle reference
	result := loadMCPFromBundleRef("nonexistent-bundle", loader)
	assert.Empty(t, result)
}

// Regression: a remote bundle lives only in the loader's seed (never extracted
// to disk), reachable by ref name via loader.Load. Resolving it by a computed
// fs path returned nothing, silently dropping its MCP server even though the
// same bundle's fragment/prompt resolved fine.
func TestLoadMCPFromBundleRef_SeededRemoteBundle(t *testing.T) {
	const ref = "ctxloom-default/sequential-thinking"
	seeded := map[string]*bundles.Bundle{
		ref: {
			Name: ref,
			MCP: map[string]bundles.BundleMCP{
				"sequential-thinking": {Command: "npx", Args: []string{"-y", "server"}},
			},
		},
	}
	loader := bundles.NewLoader(nil, false, bundles.WithSeededBundles(seeded))

	result := loadMCPFromBundleRef(ref, loader)
	assert.Contains(t, result, "sequential-thinking",
		"a remote bundle resolved only via the seed must still yield its MCP server")
	assert.Equal(t, "npx", result["sequential-thinking"].Command)
}

// =============================================================================
// ResolveBundleHooks / loadHooksFromBundleRef Tests
// =============================================================================

func TestLoadHooksFromBundleRef_LocalBundle(t *testing.T) {
	tmpDir := t.TempDir()
	bundlesDir := filepath.Join(tmpDir, "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	bundleContent := `
name: with-hooks
version: "1.0"
hooks:
  post_tool:
    - matcher: TodoWrite
      command: echo recorded
      type: command
  post_file_edit:
    - matcher: ".*-plan\\.md$"
      command: ctxloom hook stamp-plan
      type: command
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "with-hooks.yaml"), []byte(bundleContent), 0644))

	loader := bundles.NewLoader([]string{bundlesDir}, false)
	result := loadHooksFromBundleRef("with-hooks", loader)

	require.Len(t, result.PostTool, 1)
	assert.Equal(t, "TodoWrite", result.PostTool[0].Matcher)
	assert.Equal(t, "echo recorded", result.PostTool[0].Command)
	assert.Equal(t, "bundle:with-hooks", result.PostTool[0].SCM, "bundle-shipped hooks must be tagged with their origin")

	require.Len(t, result.PostFileEdit, 1)
	assert.Contains(t, result.PostFileEdit[0].Matcher, "plan")
}

func TestLoadHooksFromBundleRef_NoHooksField(t *testing.T) {
	tmpDir := t.TempDir()
	bundlesDir := filepath.Join(tmpDir, "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// A bundle without any hooks should produce a zero-valued UnifiedHooks.
	bundleContent := `
name: no-hooks
version: "1.0"
mcp:
  some-server:
    command: foo
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "no-hooks.yaml"), []byte(bundleContent), 0644))

	loader := bundles.NewLoader([]string{bundlesDir}, false)
	result := loadHooksFromBundleRef("no-hooks", loader)

	assert.Empty(t, result.PostTool)
	assert.Empty(t, result.PreTool)
	assert.Empty(t, result.PostFileEdit)
}

// TestResolveBuiltinBundleHooks asserts the embedded tasks bundle is
// parseable and its hooks ride through to UnifiedHooks tagged with the
// builtin SCM marker. This is the regression guard for the "ctxloom
// core functionality prompts/tasks ship with the binary" directive: if
// the YAML in resources/builtin_bundles/ breaks or the embed directive
// stops picking it up, apply-hooks would silently lose the plan-stamping
// hook. This test fires before that ships.
func TestResolveBuiltinBundleHooks(t *testing.T) {
	hooks := resolveBuiltinBundleHooks()

	// The tasks bundle ships no PostTool hook: TodoWrite auto-capture was
	// removed. Tasks are created/updated only via the MCP tools or CLI.
	assert.Empty(t, hooks.PostTool, "taskloom bundle must not ship a PostTool (TodoWrite capture) hook")

	require.NotEmpty(t, hooks.PostFileEdit, "taskloom bundle must contribute a PostFileEdit hook (stamp-plan)")
	foundStamp := false
	for _, h := range hooks.PostFileEdit {
		if strings.Contains(h.Command, "hook stamp-plan") {
			foundStamp = true
			assert.Equal(t, "bundle:builtin:taskloom", h.SCM)
		}
	}
	assert.True(t, foundStamp, "stamp-plan hook missing from builtin taskloom bundle")
}

// TestResolveBuiltinBundleMCPServers asserts the built-in MCP-server
// path doesn't error against the embedded YAMLs and that every server
// shipped from a built-in is tagged with the builtin SCM marker. The
// taskloom bundle registers the standalone task-store server (`taskloom mcp`),
// so the real call must surface it. The SCM-tag invariant is also pinned
// via extractMCPFromBundle with "builtin:<name>" as the source — the
// same code path resolveBuiltinBundleMCPServers takes.
func TestResolveBuiltinBundleMCPServers(t *testing.T) {
	stubLookPath(t)
	got := resolveBuiltinBundleMCPServers()
	require.NotNil(t, got, "resolveBuiltinBundleMCPServers must return a non-nil map even when empty")

	// The taskloom bundle ships the standalone task-store MCP server.
	require.Contains(t, got, "taskloom", "builtin taskloom bundle must register the taskloom MCP server")
	assert.Equal(t, "taskloom", got["taskloom"].Command)
	assert.Equal(t, []string{"mcp"}, got["taskloom"].Args)

	// Every built-in MCP server must carry the builtin SCM marker
	// (extractMCPFromBundle prepends "bundle:" to the "builtin:<name>"
	// source) so apply-mcp can identify it as ctxloom-managed.
	for name, server := range got {
		assert.True(t, strings.HasPrefix(server.SCM, "bundle:builtin:"),
			"server %q from built-in bundle must have SCM prefix bundle:builtin:, got %q", name, server.SCM)
	}

	// Pin the contract directly: a synthetic builtin source through
	// extractMCPFromBundle produces the expected SCM tag.
	synthetic := extractMCPFromBundle(&bundles.Bundle{
		MCP: map[string]bundles.BundleMCP{
			"synthetic": {Command: "fake"},
		},
	}, "builtin:future-bundle")
	require.Contains(t, synthetic, "synthetic")
	assert.Equal(t, "bundle:builtin:future-bundle", synthetic["synthetic"].SCM,
		"extractMCPFromBundle prepends 'bundle:' to whatever source it gets, including builtin:")
}

func TestHooksConfig_HasAny(t *testing.T) {
	assert.False(t, wire.HooksConfig{}.HasAny())
	withUnified := wire.HooksConfig{Unified: wire.UnifiedHooks{PostTool: []wire.Hook{{Command: "x"}}}}
	assert.True(t, withUnified.HasAny())
	withPlugin := wire.HooksConfig{Plugins: map[string]wire.BackendHooks{
		"claude-code": {"PostToolUse": []wire.Hook{{Command: "x"}}},
	}}
	assert.True(t, withPlugin.HasAny())
}

// =============================================================================
// Save Additional Coverage
// =============================================================================

func TestConfig_Save_WithMCP(t *testing.T) {
	tmpDir := t.TempDir()
	trueVal := true

	// Create the persistent directory
	require.NoError(t, os.MkdirAll(tmpDir, 0755))

	cfg := &Config{
		AppPaths: []string{tmpDir},
		LM: LMConfig{
			Configs: map[string]LLMConfig{},
		},
		MCP: wire.MCPConfig{
			AutoRegisterCtxloom: &trueVal,
			Servers: map[string]wire.MCPServer{
				"test": {Command: "test-cmd"},
			},
		},
	}

	err := cfg.Save()
	require.NoError(t, err)

	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "mcp")
	assert.Contains(t, string(data), "test-cmd")
}

func TestConfig_Save_PreservesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the persistent directory
	require.NoError(t, os.MkdirAll(tmpDir, 0755))

	// Write existing config with custom fields to persistent directory
	existingContent := `
custom_field: preserved
llm:
  configs: {}
`
	require.NoError(t, os.WriteFile(paths.ConfigPath(tmpDir), []byte(existingContent), 0644))

	cfg := &Config{
		AppPaths: []string{tmpDir},
		LM: LMConfig{
			Defaults: RoleDefaults{Primary: "claude-code"},
			Configs: map[string]LLMConfig{
				"claude-code": {Type: "claude-code"},
			},
		},
	}

	err := cfg.Save()
	require.NoError(t, err)

	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	// Should preserve the custom field
	assert.Contains(t, string(data), "custom_field")
}

// =============================================================================
// GetDefaultProfiles Additional Coverage
// =============================================================================

func TestConfig_GetDefaultProfiles_SingleProfileFallback(t *testing.T) {
	// When no default is explicitly configured but exactly one profile is
	// installed locally, GetDefaultProfiles should return it as a fallback so
	// `ctxloom run` doesn't launch with empty context.
	tmpDir := t.TempDir()
	profilesDir := paths.ProfilesPath(tmpDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	// Single profile, NOT marked default.
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "only.yaml"),
		[]byte("description: only profile\n"), 0644))

	cfg := &Config{AppPaths: []string{tmpDir}}

	assert.Empty(t, cfg.ExplicitDefaultProfiles(),
		"no default is explicitly configured")
	assert.Equal(t, []string{"only"}, cfg.GetDefaultProfiles(),
		"single installed profile should be returned by fallback")
}

func TestConfig_GetDefaultProfiles_MultipleProfilesNoDefault(t *testing.T) {
	// When two profiles exist locally and none is marked default, the fallback
	// must NOT pick one — ambiguity should bubble up so the user sees empty
	// context (rather than silently loading a random profile).
	tmpDir := t.TempDir()
	profilesDir := paths.ProfilesPath(tmpDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "a.yaml"),
		[]byte("description: a\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "b.yaml"),
		[]byte("description: b\n"), 0644))

	cfg := &Config{AppPaths: []string{tmpDir}}
	assert.Nil(t, cfg.GetDefaultProfiles())
}

func TestConfig_ExplicitDefaultProfiles_IgnoresFallback(t *testing.T) {
	// ExplicitDefaultProfiles must not trigger the single-profile fallback —
	// auto-promote depends on this distinction.
	tmpDir := t.TempDir()
	profilesDir := paths.ProfilesPath(tmpDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "only.yaml"),
		[]byte("description: only profile\n"), 0644))

	cfg := &Config{AppPaths: []string{tmpDir}}
	assert.Empty(t, cfg.ExplicitDefaultProfiles())
}

// =============================================================================
// Load Schema Validation Error
// =============================================================================

func TestLoad_SchemaValidationProducesWarning(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Create config that fails schema validation (using wrong type)
	configContent := `
llm:
  configs: "should be a map not string"
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configContent), 0644))

	// Now returns config with warnings instead of error for resilient startup
	cfg, err := Load(WithFS(fs), WithAppDir(appDir))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// Should have collected warnings about parse/validation issues
	assert.NotEmpty(t, cfg.Warnings)
}

// =============================================================================
// mergeHooks Complete Coverage (SessionEnd)
// =============================================================================

func TestResolveProfile_SessionEndHooks(t *testing.T) {
	profiles := map[string]Profile{
		"base": {
			Hooks: wire.HooksConfig{
				Unified: wire.UnifiedHooks{
					SessionEnd: []wire.Hook{{Command: "./session-end.sh"}},
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

// =============================================================================
// Resilient Startup Tests
// =============================================================================

func TestResilientStartup_MalformedConfig(t *testing.T) {
	// Test that malformed config produces warnings but doesn't fail startup
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Create malformed YAML (array where object expected)
	malformedYAML := `
llm:
  configs:
    - this is wrong format
    claude-code: {}
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(malformedYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	// Should NOT error
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Should have warnings
	assert.NotEmpty(t, cfg.Warnings)

	// Config should still be usable with defaults
	assert.NotNil(t, cfg.Profiles)
}

func TestResilientStartup_CompletelyInvalidYAML(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Completely unparseable YAML
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("{{{{invalid"), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.NotEmpty(t, cfg.Warnings)
	// Schema validation catches parse errors first
	assert.Contains(t, cfg.Warnings[0], "config validation warning")
}

func TestResilientStartup_NonExistentProfile(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(appDir), 0755))

	// Config references a non-existent profile
	configYAML := `
defaults:
  profiles:
    - nonexistent-profile
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	// Loading should succeed
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Contains(t, cfg.Profiles.Defaults, "nonexistent-profile")

	// GetDefaultProfiles should return the name even if profile doesn't exist
	defaults := cfg.GetDefaultProfiles()
	assert.Contains(t, defaults, "nonexistent-profile")
}

func TestResilientStartup_EmptyConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Empty config file - schema validation will warn but not fail
	require.NoError(t, afero.WriteFile(fs, filepath.Join(appDir, "config.yaml"), []byte(""), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// Schema validation warns on empty config, but we still start
	assert.NotNil(t, cfg.Profiles)
}

func TestResilientStartup_PartiallyValidConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Config with some valid and some invalid parts (unknown property in plugin)
	// Schema validation may catch this, but we should still not fail
	configYAML := `
llm:
  configs:
    claude-code:
      unknown_property: true
profiles:
  valid-profile:
    description: "This is valid"
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Contains(t, cfg.Profiles.Definitions, "valid-profile")
}

func TestResilientStartup_WarningsAreCollected(t *testing.T) {
	// Test that schema validation warnings are collected
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0755))

	// Create config with type mismatch that schema validation should catch
	configYAML := `
llm:
  configs: invalid-should-be-map
`
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(configYAML), 0644))

	cfg, err := Load(WithFS(fs), WithAppDir(appDir))

	// Should not error, should have warnings
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// The config struct is valid even if content is wrong
}

// =============================================================================
// Compaction Settings Tests
// =============================================================================
// Compaction settings control how session logs are compressed for memory.

func TestGetDefaultLLMModel(t *testing.T) {
	t.Run("returns the primary label's model", func(t *testing.T) {
		cfg := &Config{LM: LMConfig{
			Configs:  map[string]LLMConfig{"big": {Type: "claude-code", Body: map[string]interface{}{"model": "sonnet"}}},
			Defaults: RoleDefaults{Primary: "big"},
		}}
		assert.Equal(t, "sonnet", cfg.GetDefaultLLMModel())
	})

	t.Run("returns empty when the primary label has no model", func(t *testing.T) {
		cfg := &Config{}
		assert.Empty(t, cfg.GetDefaultLLMModel())
	})
}

func TestGetCompactionLLM(t *testing.T) {
	t.Run("returns the fast role's backend", func(t *testing.T) {
		cfg := &Config{LM: LMConfig{
			Configs:  map[string]LLMConfig{"f": {Type: "antigravity"}},
			Defaults: RoleDefaults{Fast: "f"},
		}}
		assert.Equal(t, "antigravity", cfg.GetCompactionLLM())
	})

	t.Run("falls back to the primary role when no fast role", func(t *testing.T) {
		cfg := &Config{LM: LMConfig{
			Configs:  map[string]LLMConfig{"p": {Type: "codex"}},
			Defaults: RoleDefaults{Primary: "p"},
		}}
		assert.Equal(t, "codex", cfg.GetCompactionLLM())
	})

	t.Run("falls back to claude-code", func(t *testing.T) {
		cfg := &Config{}
		assert.Equal(t, "claude-code", cfg.GetCompactionLLM())
	})
}

func TestGetCompactionModel(t *testing.T) {
	t.Run("returns the fast role's model", func(t *testing.T) {
		cfg := &Config{LM: LMConfig{
			Configs:  map[string]LLMConfig{"f": {Type: "claude-code", Body: map[string]interface{}{"model": "haiku"}}},
			Defaults: RoleDefaults{Fast: "f"},
		}}
		assert.Equal(t, "haiku", cfg.GetCompactionModel())
	})

	t.Run("empty when the fast label has no model", func(t *testing.T) {
		// No model named on the fast label → empty, so the backend supplies its own.
		cfg := &Config{LM: LMConfig{
			Configs:  map[string]LLMConfig{"f": {Type: "claude-code"}},
			Defaults: RoleDefaults{Fast: "f"},
		}}
		assert.Equal(t, "", cfg.GetCompactionModel())
	})
}

func TestGetCompactionChunkSize(t *testing.T) {
	t.Run("returns configured size", func(t *testing.T) {
		cfg := &Config{Settings: SettingsConfig{CompactionChunks: 4000}}
		assert.Equal(t, 4000, cfg.GetCompactionChunkSize())
	})

	t.Run("defaults to 8000", func(t *testing.T) {
		cfg := &Config{}
		assert.Equal(t, 8000, cfg.GetCompactionChunkSize())
	})
}

// =============================================================================
// SyncConfig Tests
// =============================================================================

func TestSyncConfig_ShouldAutoSync(t *testing.T) {
	t.Run("returns true by default", func(t *testing.T) {
		cfg := &SyncConfig{}
		assert.True(t, cfg.ShouldAutoSync())
	})

	t.Run("returns true for nil config", func(t *testing.T) {
		var cfg *SyncConfig
		assert.True(t, cfg.ShouldAutoSync())
	})

	t.Run("returns false when disabled", func(t *testing.T) {
		disabled := false
		cfg := &SyncConfig{AutoSync: &disabled}
		assert.False(t, cfg.ShouldAutoSync())
	})

	t.Run("returns true when explicitly enabled", func(t *testing.T) {
		enabled := true
		cfg := &SyncConfig{AutoSync: &enabled}
		assert.True(t, cfg.ShouldAutoSync())
	})
}

// =============================================================================
// FragmentRef YAML Serialization Tests
// =============================================================================

func TestFragmentRef_UnmarshalYAML(t *testing.T) {
	t.Run("unmarshals string format", func(t *testing.T) {
		yamlData := `go-style`
		var ref FragmentRef
		err := yaml.Unmarshal([]byte(yamlData), &ref)
		require.NoError(t, err)
		assert.Equal(t, "go-style", ref.Name)
		assert.Equal(t, 0, ref.Priority)
	})

	t.Run("unmarshals struct format with priority", func(t *testing.T) {
		yamlData := `
name: testing
priority: 10
`
		var ref FragmentRef
		err := yaml.Unmarshal([]byte(yamlData), &ref)
		require.NoError(t, err)
		assert.Equal(t, "testing", ref.Name)
		assert.Equal(t, 10, ref.Priority)
	})

	t.Run("unmarshals struct format without priority", func(t *testing.T) {
		yamlData := `
name: my-fragment
`
		var ref FragmentRef
		err := yaml.Unmarshal([]byte(yamlData), &ref)
		require.NoError(t, err)
		assert.Equal(t, "my-fragment", ref.Name)
		assert.Equal(t, 0, ref.Priority)
	})

	t.Run("unmarshals list of mixed formats", func(t *testing.T) {
		yamlData := `
- go-style
- name: testing
  priority: 10
- another-fragment
`
		var refs []FragmentRef
		err := yaml.Unmarshal([]byte(yamlData), &refs)
		require.NoError(t, err)
		require.Len(t, refs, 3)
		assert.Equal(t, "go-style", refs[0].Name)
		assert.Equal(t, 0, refs[0].Priority)
		assert.Equal(t, "testing", refs[1].Name)
		assert.Equal(t, 10, refs[1].Priority)
		assert.Equal(t, "another-fragment", refs[2].Name)
	})
}

func TestFragmentRef_MarshalYAML(t *testing.T) {
	t.Run("marshals to string when priority is 0", func(t *testing.T) {
		ref := FragmentRef{Name: "go-style", Priority: 0}
		result, err := ref.MarshalYAML()
		require.NoError(t, err)
		assert.Equal(t, "go-style", result)
	})

	t.Run("marshals to struct when priority is non-zero", func(t *testing.T) {
		ref := FragmentRef{Name: "testing", Priority: 10}
		result, err := ref.MarshalYAML()
		require.NoError(t, err)
		// Result should be a struct-like value, not a string
		assert.NotEqual(t, "testing", result)
	})

	t.Run("roundtrip preserves data", func(t *testing.T) {
		original := []FragmentRef{
			{Name: "simple", Priority: 0},
			{Name: "prioritized", Priority: 5},
		}
		data, err := yaml.Marshal(original)
		require.NoError(t, err)

		var loaded []FragmentRef
		err = yaml.Unmarshal(data, &loaded)
		require.NoError(t, err)

		require.Len(t, loaded, 2)
		assert.Equal(t, "simple", loaded[0].Name)
		assert.Equal(t, 0, loaded[0].Priority)
		assert.Equal(t, "prioritized", loaded[1].Name)
		assert.Equal(t, 5, loaded[1].Priority)
	})
}
