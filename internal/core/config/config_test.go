package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/convert"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// withDefaultProfiles points cfg's default profile set at names by binding a
// "default" agent and pointing DefaultAgent at it — the replacement for the
// retired profiles.defaults fixture. DefaultAgentProfiles reads
// the default agent's composed profiles.
func withDefaultProfiles(cfg *Config, names ...string) *Config {
	cfg.defaultAgent = "default"
	if cfg.agents == nil {
		cfg.agents = map[string]agents.Agent{}
	}
	cfg.agents["default"] = agents.Agent{Profiles: names}
	return cfg
}

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
// - MCP servers can be scoped to specific backends (claude-code, codex)
// - Config is fault-tolerant: invalid entries warn but don't block startup
//
// =============================================================================

// =============================================================================
// Default Plugin Tests
// =============================================================================
// The default LLM plugin determines which AI backend is used when none is
// explicitly specified. With no label resolving, the engine the config was
// validated against as the registry's default answers; an unvalidated
// config has no default engine and resolves to "", which every downstream
// consumer refuses loudly as an unknown backend.

func TestGetDefaultLLM(t *testing.T) {
	t.Run("resolves the primary label's backend type", func(t *testing.T) {
		cfg := &Config{lm: LMConfig{
			Configs: map[string]LLMConfig{
				"big": {Type: "mock", Body: map[string]interface{}{"model": "pro"}},
			},
			Defaults: RoleDefaults{Primary: "big"},
		}}
		assert.Equal(t, "mock", cfg.GetDefaultLLM())
	})

	t.Run("returns the bound default engine when no label resolves", func(t *testing.T) {
		cfg := &Config{defaultEngine: "fixture-default"}
		assert.Equal(t, "fixture-default", cfg.GetDefaultLLM())
	})

	t.Run("an unvalidated config has no default engine", func(t *testing.T) {
		cfg := &Config{}
		assert.Empty(t, cfg.GetDefaultLLM())
	})
}

// PrimaryLabel falls back to the sole configured label when no role default is
// set, so a single-config project resolves without naming a role.
func TestPrimaryLabel_SingleConfigFallback(t *testing.T) {
	cfg := &Config{lm: LMConfig{Configs: map[string]LLMConfig{
		"only": {Type: "claude-code"},
	}}}
	assert.Equal(t, "only", cfg.PrimaryLabel())
}

// FastLabel falls back to the primary label when no fast role is set.
func TestFastLabel_FallsBackToPrimary(t *testing.T) {
	cfg := &Config{lm: LMConfig{Defaults: RoleDefaults{Primary: "big"}}}
	assert.Equal(t, "big", cfg.FastLabel())
}

// ResolveLLM reads backend type + model straight from the labeled entry; an
// unknown label degrades to the built-in default backend with no model.
func TestResolveLLM(t *testing.T) {
	cfg := &Config{defaultEngine: "fixture-default", lm: LMConfig{Configs: map[string]LLMConfig{
		"g":       {Type: "mock", Body: map[string]interface{}{"model": "gemini-3-pro"}},
		"bare":    {Type: "claude-code"},
		"untyped": {Body: map[string]interface{}{"model": "m"}},
	}}}

	backend, model := cfg.ResolveLLM("g")
	assert.Equal(t, "mock", backend)
	assert.Equal(t, "gemini-3-pro", model)

	backend, model = cfg.ResolveLLM("bare")
	assert.Equal(t, "claude-code", backend)
	assert.Empty(t, model)

	backend, model = cfg.ResolveLLM("untyped")
	assert.Equal(t, "fixture-default", backend, "an untyped entry drives the bound default engine")
	assert.Equal(t, "m", model)

	backend, model = cfg.ResolveLLM("missing")
	assert.Equal(t, "fixture-default", backend, "unknown label degrades to the bound default engine")
	assert.Empty(t, model)
}

// A bare (empty-string) label must honour the project's configured primary —
// it is what a caller like `container check`/`container build` passes when
// invoked with no backend named, and unsent-refinish ruled that a bare
// invocation must resolve to llm.defaults.primary rather than silently
// falling through to the built-in default backend.
func TestResolveLLM_EmptyLabelHonoursPrimary(t *testing.T) {
	cfg := &Config{lm: LMConfig{
		Configs: map[string]LLMConfig{
			"mock": {Type: "mock", Body: map[string]interface{}{"model": "test-model"}},
		},
		Defaults: RoleDefaults{Primary: "mock"},
	}}

	backend, model := cfg.ResolveLLM("")
	assert.Equal(t, "mock", backend, "empty label must resolve through cfg.PrimaryLabel(), not a raw map miss")
	assert.Equal(t, "test-model", model)
}

// With no primary resolvable (no defaults.primary and not exactly one
// configured label), an empty label must still degrade to the built-in
// default backend rather than crash or loop.
func TestResolveLLM_EmptyLabelNoDefaultDegradesToBuiltin(t *testing.T) {
	cfg := &Config{defaultEngine: "fixture-default", lm: LMConfig{Configs: map[string]LLMConfig{
		"a": {Type: "mock"},
		"b": {Type: "claude-code"},
	}}}

	backend, model := cfg.ResolveLLM("")
	assert.Equal(t, "fixture-default", backend, "no resolvable primary: empty label degrades to the bound default engine")
	assert.Empty(t, model)
}

// =============================================================================
// Profile Resolution Tests
// =============================================================================
// Profile resolution handles inheritance chains and merges settings from
// parent profiles. This enables composition of reusable profile fragments.

// =============================================================================
// Profile Exclusion Tests
// =============================================================================
// GetEditorCommand Tests
// =============================================================================

func TestConfig_GetEditorCommand(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		visual   string
		editor   string
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "config takes precedence",
			config:   &Config{editor: EditorConfig{Command: "vim", Args: []string{"-n"}}},
			visual:   "code",
			editor:   "nano",
			wantCmd:  "vim",
			wantArgs: []string{"-n"},
		},
		{
			name:     "VISUAL fallback",
			config:   &Config{},
			visual:   "code",
			editor:   "nano",
			wantCmd:  "code",
			wantArgs: nil,
		},
		{
			name:     "EDITOR fallback",
			config:   &Config{},
			visual:   "",
			editor:   "emacs",
			wantCmd:  "emacs",
			wantArgs: nil,
		},
		{
			name:     "default to nano",
			config:   &Config{},
			visual:   "",
			editor:   "",
			wantCmd:  "nano",
			wantArgs: nil,
		},
		{
			// "code --wait" must split into binary + flag, not be exec'd as
			// one binary named "code --wait" (which never exists).
			name:     "EDITOR with flags is split",
			config:   &Config{},
			editor:   "code --wait",
			wantCmd:  "code",
			wantArgs: []string{"--wait"},
		},
		{
			name:     "VISUAL with flags wins over EDITOR",
			config:   &Config{},
			visual:   "emacsclient -t",
			editor:   "nano",
			wantCmd:  "emacsclient",
			wantArgs: []string{"-t"},
		},
		{
			// A multi-word config command splits too, with editor.args
			// appended after the inline flags.
			name:     "config command with flags plus args",
			config:   &Config{editor: EditorConfig{Command: "code --wait", Args: []string{"-n"}}},
			editor:   "vim",
			wantCmd:  "code",
			wantArgs: []string{"--wait", "-n"},
		},
		{
			// A blank (whitespace-only) config command is no choice at all and
			// falls through to the environment.
			name:     "blank config command falls back to env",
			config:   &Config{editor: EditorConfig{Command: "   "}},
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
// `config edit`): VISUAL → EDITOR → nano, with whitespace splitting.
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

func TestConfig_GetConfigFilePath(t *testing.T) {
	t.Run("returns path when AppPaths set", func(t *testing.T) {
		cfg := &Config{appPaths: []string{"/path/to/.ctxloom"}}
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

// =============================================================================
// Config Save Tests
// =============================================================================

func TestConfig_Save(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the persistent directory
	require.NoError(t, os.MkdirAll(tmpDir, 0755))

	cfg := &Config{
		appPaths: []string{tmpDir},
		lm: LMConfig{
			Defaults: RoleDefaults{Primary: "claude-code"},
			Configs: map[string]LLMConfig{
				"claude-code": {Type: "claude-code"},
			},
		},
		defaultAgent: "dev",
		agents: map[string]agents.Agent{
			"dev": {Profiles: []string{"dev"}},
		},
	}

	err := cfg.saveLocked(cfg.getFS(), paths.ConfigPath(tmpDir))
	require.NoError(t, err)

	// Verify file was written to persistent dir
	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "claude-code")
	assert.Contains(t, string(data), "default_agent: dev", "default_agent round-trips through a save")
	assert.Contains(t, string(data), "- dev")
	assert.Contains(t, string(data), "llm")
}

// =============================================================================
// Load and LoadOption Tests
// =============================================================================

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
// DefaultAgentProfiles Tests
// =============================================================================

func TestConfig_DefaultAgentProfiles(t *testing.T) {
	t.Run("returns the default agent's profiles", func(t *testing.T) {
		cfg := withDefaultProfiles(&Config{}, "dev")
		assert.Equal(t, []string{"dev"}, cfg.DefaultAgentProfiles())
	})

	t.Run("returns nil when no default agent", func(t *testing.T) {
		cfg := &Config{}
		assert.Nil(t, cfg.DefaultAgentProfiles())
	})

	t.Run("returns nil when default_agent names an undefined agent", func(t *testing.T) {
		cfg := &Config{defaultAgent: "missing"}
		assert.Nil(t, cfg.DefaultAgentProfiles())
	})

	t.Run("returns multiple composed profiles verbatim", func(t *testing.T) {
		cfg := withDefaultProfiles(&Config{}, "profile1", "profile2", "profile3")
		assert.Equal(t, []string{"profile1", "profile2", "profile3"}, cfg.DefaultAgentProfiles())
	})
}

// =============================================================================
// GetProfileLoader Tests
// =============================================================================

func TestConfig_GetProfileLoader(t *testing.T) {
	cfg := &Config{
		appPaths: []string{"/project/.ctxloom"},
	}

	loader := cfg.GetProfileLoader()

	assert.NotNil(t, loader)
}

// =============================================================================
// ResolveProfile - addFragment and addBundleItem Coverage
// =============================================================================

// =============================================================================
// mergeHooks Coverage - PreShell and PostFileEdit
// =============================================================================

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

	result := extractMCPFromBundle(report.Reporter{}, bundles.ProjectAuthoredRead("fixture", bundle), mustLocalRef(t, "my-bundle"), composite.Ungated().Authorizer())

	assert.Len(t, result, 1)
	assert.Equal(t, "test-cmd", result["test-server"].Command)
	assert.Equal(t, []string{"--arg1"}, result["test-server"].Args)
	assert.Equal(t, "value", result["test-server"].Env["KEY"])
	assert.Equal(t, "Test server", result["test-server"].Notes)
	assert.Equal(t, "bundle:ctxloom+local:my-bundle", result["test-server"].SCM)
}

// =============================================================================
// resolveProfileRecursive Depth Limit
// =============================================================================

// =============================================================================
// ResolveBundleMCPServers Tests
// =============================================================================

// onlyBuiltinMCPServers asserts a resolution surfaced nothing beyond the
// embedded builtin bundles' servers. Exactly one exists — ctxloom's own, from
// resources/builtin_bundles/ctxloom-mcp.yaml — and it must be there whatever
// the profile scope: builtins are injected unconditionally, which is what makes
// composing that bundle the thing that registers ctxloom's MCP server.
// (Companion loadouts also land here; none of these callers fakes a loadout
// probe, so none appears.)
func onlyBuiltinMCPServers(t *testing.T, result map[string]wire.MCPServer) {
	t.Helper()
	for name, server := range result {
		assert.True(t, strings.HasPrefix(server.SCM, "bundle:ctxloom+builtin:") || strings.HasPrefix(server.SCM, "bundle:ctxloom+companion:"),
			"unexpected non-builtin, non-companion MCP server %q (SCM %q)", name, server.SCM)
	}
	own, ok := result["ctxloom"]
	require.True(t, ok, "ctxloom's own MCP server must be injected by the builtin ctxloom bundle; got %v", result)
	assert.Equal(t, "bundle:ctxloom+builtin:ctxloom-mcp", own.SCM)
	assert.Equal(t, []string{"mcp", "serve"}, own.Args, "the builtin entry must invoke the `mcp serve` leaf")
	assert.Len(t, result, 1, "no other embedded builtin bundle ships an MCP server, and these callers don't fake a companion loadout probe")
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
		appPaths: []string{"/project/.ctxloom"},
	}
	cfg.BindTrustForTesting(compositetest.Trust())

	onlyBuiltinMCPServers(t, cfg.ResolveBundleMCPServers(nil))
}

func TestConfig_ResolveBundleMCPServers_NoAppPaths(t *testing.T) {
	stubLookPath(t)
	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"test"}}},
		appPaths: []string{},
	}
	cfg.BindTrustForTesting(compositetest.Trust())

	onlyBuiltinMCPServers(t, cfg.ResolveBundleMCPServers(nil))
}

// An unresolvable profile (`ctxloom run -p <typo>`) delivers zero MCP
// servers, zero hooks, zero commands and zero skills. That empty result is not
// a legitimate "nothing configured" — it is "we could not work out what to
// deliver" — so every one of the four bundle resolvers must say so rather than
// `continue` past it. Previously this test asserted only the empty map, which
// is what the silent no-op produces.
func TestConfig_ResolveBundleMCPServers_ProfileNotFound(t *testing.T) {
	stubLookPath(t)
	resetConfigStrictness(t)
	fs := afero.NewMemMapFs()
	appDir := "/project/.ctxloom"
	require.NoError(t, fs.MkdirAll(filepath.Join(appDir, "profiles"), 0755))

	newCfg := func() *Config {
		cfg := &Config{
			defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"nonexistent"}}},
			appPaths: []string{appDir},
			fs:       fs,
			rep:      ledgerReporter(),
		}
		cfg.BindTrustForTesting(compositetest.Trust())
		return cfg
	}

	mark := strictness.Checkpoint()
	onlyBuiltinMCPServers(t, newCfg().ResolveBundleMCPServers(nil))
	found := strictness.Since(mark)
	require.NotEmpty(t, found, "an unresolvable profile must record a finding, not vanish")
	assert.Equal(t, strictness.ClassRef, found[0].Class)
	assert.Contains(t, found[0].Message, "nonexistent")

	// The other three resolvers share the defect and must share the fix.
	// FailOnce dedups per formatted message, so each is checked in its own
	// window against a fresh Config (the loaders memoize per Config).
	for _, tc := range []struct {
		name string
		call func(*Config)
	}{
		{"hooks", func(c *Config) { c.ResolveBundleHooks(nil) }},
		{"commands", func(c *Config) { c.ResolveBundleCommands(nil) }},
		{"skills", func(c *Config) { c.ResolveBundleSkills(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetConfigStrictness(t)
			mark := strictness.Checkpoint()
			tc.call(newCfg())
			assert.NotEmpty(t, strictness.Since(mark),
				"%s: an unresolvable profile must be reported here too", tc.name)
		})
	}
}

// A bundle ref that fails to load drops ALL of its MCP servers and hooks. The
// error was thrown away, so the result was indistinguishable from a bundle
// that ships neither — no warning, no finding, exit 0. The sibling
// loadBundleProfileSeed (config.go) already reports exactly this fault.
func TestConfig_BundleRefThatFailsToLoadIsReported(t *testing.T) {
	stubLookPath(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))
	// A ref the profile names but that is nowhere on disk: loader.Load fails in
	// Find. Deliberately NOT a malformed local bundle file — the loader's
	// directory scan already reports those itself, which would let this test
	// pass without loadMCPFromBundleRef/loadHooksFromBundleRef reporting
	// anything (a false green: verified by running it against the unfixed
	// source).
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "p.yaml"),
		[]byte("name: p\nbundles:\n  - absent-bundle\n"), 0644))

	newCfg := func() *Config {
		return &Config{
			defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
			appPaths: []string{appDir},
			rep:      ledgerReporter(),
		}
	}

	for _, tc := range []struct {
		name string
		call func(*Config)
	}{
		{"mcp", func(c *Config) { c.ResolveBundleMCPServers(nil) }},
		{"hooks", func(c *Config) { c.ResolveBundleHooks(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetConfigStrictness(t)
			mark := strictness.Checkpoint()
			tc.call(newCfg())
			found := strictness.Since(mark)
			require.NotEmpty(t, found,
				"%s: a bundle that failed to load must be reported, not silently contribute nothing", tc.name)
			assert.Equal(t, strictness.ClassBundle, found[0].Class)
			assert.Contains(t, found[0].Message, "absent-bundle")
		})
	}
}

// The other side of the discriminator. A profile's `bundles:` list may carry
// ITEM-SCOPED refs ("<bundle>#fragments/<name>") that select one fragment out
// of a bundle. loader.Load cannot resolve those by design, and a selector that
// picked one fragment SHOULD contribute no MCP servers and no hooks — that is
// a legitimate empty result, not a swallowed failure, and must not be reported
// as a fatal startup finding.
func TestConfig_ItemScopedBundleRefIsNotAFailure(t *testing.T) {
	stubLookPath(t)
	resetConfigStrictness(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "local.yaml"),
		[]byte("version: \"1.0\"\nfragments:\n  onboarding:\n    content: hi\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "p.yaml"),
		[]byte("name: p\nbundles:\n  - local#fragments/onboarding\n"), 0644))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
		appPaths: []string{appDir},
	}
	cfg.BindTrustForTesting(compositetest.Trust())

	mark := strictness.Checkpoint()
	cfg.ResolveBundleMCPServers(nil)
	cfg.ResolveBundleHooks(nil)
	assert.Empty(t, strictness.Since(mark),
		"a fragment-scoped ref shipping no MCP servers or hooks is correct, not a fault")
}

// resetConfigStrictness gives a test pristine strict-mode state and stops the
// package-global finding collector bleeding into its neighbours.
func resetConfigStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
}

// NOTE: the embedded-builtin companion-gating tests that used to live here
// (TestResolveBuiltinBundleMCPServers_MissingBinarySkipped,
// TestResolveBuiltinBundleFragments_CompanionGating,
// TestResolveBuiltinBundleHooks_CompanionGating) drove resolveBuiltinBundleHooks
// / resolveBuiltinBundleMCPServers / ResolveBuiltinBundleFragments against the
// REAL embedded resources/builtin_bundles/{ltk,taskloom}.yaml fixtures. S8
// deleted those fixtures — ltk/taskloom now contribute this same content via
// their own LOADOUTS, discovered on PATH, not embedded in the binary. The
// equivalent coverage (present/absent/probe-failure, content, and gating —
// including the property that a DENYING gate withholds companion content,
// which a true builtin exemption would NOT) now lives in
// companion_loadout_test.go: see
// TestProbeCompanionLoadouts_* (discovery/verify/parse),
// TestResolveBundleHooks_IncludesCompanionLoadoutHooks_Gated,
// TestResolveBundleMCPServers_IncludesCompanionLoadoutServers_Gated, and
// TestResolveBuiltinBundleFragments_IncludesCompanionFragments_Gated.

// Regression: a bundle reachable only through profile inheritance must still
// have its MCP server resolved. Before the fix, ResolveBundleMCPServers read a
// flat profileLoader.Load(profile).Bundles — which omits inherited bundles —
// so a parent's bundle MCP server was silently dropped (while its fragment and
// prompt were still exported through other paths).
func TestConfig_ResolveBundleMCPServers_InheritedBundle(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2) // committed content tree
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// Parent profile ships the bundle; child only inherits and is the default.
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "parent.yaml"),
		[]byte("name: parent\nbundles:\n  - seq-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "child.yaml"),
		[]byte("name: child\nparents:\n  - parent\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "seq-bundle.yaml"),
		[]byte("version: \"1.0\"\nmcp:\n  sequential-thinking:\n    command: npx\n    args: [\"-y\", \"server\"]\n"), 0644))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"child"}}},
		appPaths: []string{appDir},
	}
	cfg.BindTrustForTesting(compositetest.Trust())

	result := cfg.ResolveBundleMCPServers(nil)
	assert.Contains(t, result, "sequential-thinking",
		"MCP server from a parent-inherited bundle should resolve")
}

// Regression: a directory profile's exclude_mcp must filter bundle-shipped
// servers, matching the name-based filter the inline config-profile path
// applies in profileBuilder.toProfile.
func TestConfig_ResolveBundleMCPServers_ExcludeMCP(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2) // committed content tree
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
		[]byte("name: dev\nbundles:\n  - mcp-bundle\nexclude_mcp:\n  - noisy-server\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "mcp-bundle.yaml"),
		[]byte("version: \"1.0\"\nmcp:\n  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n"), 0644))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
		appPaths: []string{appDir},
	}
	cfg.BindTrustForTesting(compositetest.Trust())

	result := cfg.ResolveBundleMCPServers(nil)
	assert.Contains(t, result, "quiet-server",
		"non-excluded MCP server should resolve")
	assert.NotContains(t, result, "noisy-server",
		"exclude_mcp server should be filtered out")
}

// TestConfig_ResolveBundle_ScopesToSelectedProfile pins the per-agent config
// retarget: passing an explicit profile set scopes bundle MCP AND prompts/commands
// to THAT profile's bundles, distinct from the configured defaults. This is the
// fix for `run -p X` leaking the default profile's MCP and every pulled bundle's
// commands into X's session.
func TestConfig_ResolveBundle_ScopesToSelectedProfile(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2) // committed content tree
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "developer.yaml"),
		[]byte("name: developer\nbundles:\n  - dev-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "finder.yaml"),
		[]byte("name: finder\nbundles:\n  - finder-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "dev-bundle.yaml"),
		[]byte("version: \"1.0\"\nmcp:\n  dev-mcp:\n    command: npx\n    args: [\"-y\", \"dev\"]\ncommands:\n  dev-skill:\n    description: d\n    content: c\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "finder-bundle.yaml"),
		[]byte("version: \"1.0\"\nmcp:\n  finder-mcp:\n    command: npx\n    args: [\"-y\", \"finder\"]\ncommands:\n  finder-skill:\n    description: f\n    content: c\n"), 0644))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"developer"}}},
		appPaths: []string{appDir},
	}
	cfg.BindTrustForTesting(compositetest.Trust())

	// Selecting finder scopes MCP to finder's bundle only — NOT the default
	// (developer) profile's.
	selMCP := cfg.ResolveBundleMCPServers([]string{"finder"})
	assert.Contains(t, selMCP, "finder-mcp")
	assert.NotContains(t, selMCP, "dev-mcp", "selecting finder must not pull the default profile's MCP")

	// nil falls back to the configured default (developer) — the manage/apply path.
	defMCP := cfg.ResolveBundleMCPServers(nil)
	assert.Contains(t, defMCP, "dev-mcp")
	assert.NotContains(t, defMCP, "finder-mcp")

	// Same scoping for prompts/commands — the formerly-global surface.
	var selCommands []string
	for _, lc := range cfg.ResolveBundleCommands([]string{"finder"}) {
		selCommands = append(selCommands, lc.Item)
	}
	assert.Contains(t, selCommands, "finder-skill")
	assert.NotContains(t, selCommands, "dev-skill", "selecting finder must not pull every bundle's commands")
}

// hookBundleYAML is a bundle that ships one hook per several event types, used
// to assert the profile-gated ResolveBundleHooks path surfaces bundle hooks
// tagged with the bundle's SCM marker.
const hookBundleYAML = `
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
	const bundleSCM = "bundle:ctxloom+local:hook-bundle"

	newProject := func(t *testing.T) (appDir, profilesDir, bundlesDir string) {
		t.Helper()
		appDir = filepath.Join(t.TempDir(), ".ctxloom")
		profilesDir = filepath.Join(appDir, "profiles")
		bundlesDir = paths.LocalBundlesPathFor(appDir, paths.LayoutV2) // committed content tree
		require.NoError(t, os.MkdirAll(profilesDir, 0755))
		require.NoError(t, os.MkdirAll(bundlesDir, 0755))
		return appDir, profilesDir, bundlesDir
	}

	t.Run("direct profile reference surfaces bundle hooks", func(t *testing.T) {
		appDir, profilesDir, bundlesDir := newProject(t)
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hook-bundle.yaml"), []byte(hookBundleYAML), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
			[]byte("name: dev\nbundles:\n  - hook-bundle\n"), 0644))

		cfg := &Config{defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, appPaths: []string{appDir}}
		cfg.BindTrustForTesting(compositetest.Trust())
		result := cfg.ResolveBundleHooks(nil)

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

		cfg := &Config{defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"child"}}}, appPaths: []string{appDir}}
		cfg.BindTrustForTesting(compositetest.Trust())
		result := cfg.ResolveBundleHooks(nil)

		assert.True(t, hasHookCommand(result.PreTool, "echo pre-tool", bundleSCM),
			"a hook from a parent-inherited bundle must resolve (recursive ResolveProfile)")
	})

	t.Run("unresolvable profile and bundle ref are skipped", func(t *testing.T) {
		appDir, profilesDir, _ := newProject(t)
		// A default profile that does not exist (ResolveProfile errors → skip) and
		// a profile referencing a bundle that is not on disk (Load errors → skip).
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "real.yaml"),
			[]byte("name: real\nbundles:\n  - ghost-bundle\n"), 0644))

		cfg := &Config{defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"missing", "real"}}}, appPaths: []string{appDir}}
		cfg.BindTrustForTesting(compositetest.Trust())
		result := cfg.ResolveBundleHooks(nil)

		assert.False(t, hasHookCommand(result.PreTool, "echo pre-tool", bundleSCM),
			"a ghost bundle ref contributes no hooks")
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
version: "1.0"
mcp:
  test-server:
    command: test-cmd
    args: ["--arg"]
`
	// bundlesDir is the SEARCH root the reader layers into its format roots;
	// the bundle goes in the v2 root beneath it, or nothing reads it.
	v2Dir := paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(v2Dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(v2Dir, "test-bundle.yaml"), []byte(bundleContent), 0644))

	loader := bundles.NewLoader(bundles.NewProjectReader(nil, []string{bundlesDir}))
	result := loadMCPFromBundleRef(report.Reporter{}, "test-bundle", loader.Catalog(), composite.Ungated().Authorizer())

	assert.Len(t, result, 1)
	assert.Equal(t, "test-cmd", result["test-server"].Command)
}

func TestLoadMCPFromBundleRef_InvalidRef(t *testing.T) {
	tmpDir := t.TempDir()
	loader := bundles.NewLoader(bundles.NewProjectReader(nil, []string{tmpDir}))

	// Invalid bundle reference
	result := loadMCPFromBundleRef(report.Reporter{}, "nonexistent-bundle", loader.Catalog(), composite.Ungated().Authorizer())
	assert.Empty(t, result)
}

// Regression: a remote bundle lives only in the loader's seed (never extracted
// to disk), reachable by ref name via loader.Load. Resolving it by a computed
// fs path returned nothing, silently dropping its MCP server even though the
// same bundle's fragment/prompt resolved fine.
func TestLoadMCPFromBundleRef_SeededRemoteBundle(t *testing.T) {
	const ref = "ctxloom-default/sequential-thinking"
	// Pinned remote content reaches the loader through a repofs reader over the
	// bytes at its pinned revision — the same path the lockfile takes — so the
	// test cannot mint a provenance no reader would have produced.
	// A TREE, staged through the production converter: the MCP entry is a file
	// beside the envelope, which is the only shape a remote bundle has.
	const root = "/pinned"
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll(root, 0o755))
	st, err := content.NewTreeStore(fsys, root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, convert.Convert(context.Background(), st, content.BundleID("sequential-thinking"),
		&bundles.Bundle{
			Version: "1.0",
			MCP: map[string]bundles.BundleMCP{
				"sequential-thinking": {Command: "npx", Args: []string{"-y", "server"}},
			},
		}, convert.Options{}))
	tree, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	loader := bundles.NewLoader(bundles.NewRepoFSReader(tree, ref, bundles.WithRepoURL("https://example.test/repo")))

	result := loadMCPFromBundleRef(report.Reporter{}, ref, loader.Catalog(), composite.Ungated().Authorizer())
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
	v2Dir := paths.BundlesLayoutRoot(bundlesDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(v2Dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(v2Dir, "with-hooks.yaml"), []byte(bundleContent), 0644))

	loader := bundles.NewLoader(bundles.NewProjectReader(nil, []string{bundlesDir}))
	result := loadHooksFromBundleRef(report.Reporter{}, "with-hooks", loader.Catalog(), composite.Ungated().Authorizer(), bundles.LinksUnchecked())

	require.Len(t, result.PostTool, 1)
	assert.Equal(t, "TodoWrite", result.PostTool[0].Matcher)
	assert.Equal(t, "echo recorded", result.PostTool[0].Command)
	assert.Equal(t, "bundle:ctxloom+local:with-hooks", result.PostTool[0].SCM, "bundle-shipped hooks must be tagged with their origin")

	require.Len(t, result.PostFileEdit, 1)
	assert.Contains(t, result.PostFileEdit[0].Matcher, "plan")
}

func TestLoadHooksFromBundleRef_NoHooksField(t *testing.T) {
	tmpDir := t.TempDir()
	bundlesDir := filepath.Join(tmpDir, "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0755))

	// A bundle without any hooks should produce a zero-valued UnifiedHooks.
	bundleContent := `
version: "1.0"
mcp:
  some-server:
    command: foo
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "no-hooks.yaml"), []byte(bundleContent), 0644))

	loader := bundles.NewLoader(bundles.NewProjectReader(nil, []string{bundlesDir}))
	result := loadHooksFromBundleRef(report.Reporter{}, "no-hooks", loader.Catalog(), composite.Ungated().Authorizer(), bundles.LinksUnchecked())

	assert.Empty(t, result.PostTool)
	assert.Empty(t, result.PreTool)
	assert.Empty(t, result.PostFileEdit)
}

// TestResolveBuiltinBundleHooks proves the embedded-builtin-bundle hook path
// degrades cleanly to a zero-valued UnifiedHooks now that no embedded bundle
// ships a companion wire-in (S8 deleted resources/builtin_bundles/{ltk,
// taskloom}.yaml — that content now rides their own loadouts, discovered on
// PATH; see TestResolveBundleHooks_IncludesCompanionLoadoutHooks_Gated). The
// SCM-tagging contract for any FUTURE embedded builtin is still pinned
// directly via a synthetic bundle through extractHooksFromBundle — the exact
// code path resolveBuiltinBundleHooks takes.
func TestResolveBuiltinBundleHooks(t *testing.T) {
	hooks := resolveBuiltinBundleHooks(report.Reporter{}, composite.Ungated().Authorizer(), bundles.LinksUnchecked())
	assert.Empty(t, hooks.PreTool)
	assert.Empty(t, hooks.PostTool)
	assert.Empty(t, hooks.SessionStart)
	assert.Empty(t, hooks.SessionEnd)
	assert.Empty(t, hooks.PreShell)
	assert.Empty(t, hooks.PostFileEdit, "no embedded builtin bundle ships a hook anymore")

	synthetic := extractHooksFromBundle(report.Reporter{}, bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{
		Hooks: bundles.BundleHooks{PostFileEdit: []bundles.BundleHook{{Command: "echo hi", Type: "command"}}},
	}), mustBuiltinRef(t, "future-bundle"), composite.Ungated().Authorizer(), bundles.LinksUnchecked())
	require.Len(t, synthetic.PostFileEdit, 1)
	assert.Equal(t, "bundle:ctxloom+builtin:future-bundle", synthetic.PostFileEdit[0].SCM,
		"extractHooksFromBundle prepends 'bundle:' to the source's canonical BundleIdentity, including a builtin's")
}

// TestResolveBuiltinBundleMCPServers proves the embedded-builtin-bundle
// MCP-server path DELIVERS: ctxloom's own server ships in
// resources/builtin_bundles/ctxloom-mcp.yaml, and this resolver — which every
// session runs unconditionally, before any profile scope — is what puts it in
// the managed set. The SCM-tag contract is pinned both through the real bundle
// and directly via extractMCPFromBundle with a BuiltinRef as the source.
func TestResolveBuiltinBundleMCPServers(t *testing.T) {
	stubLookPath(t)
	got := resolveBuiltinBundleMCPServers(report.Reporter{}, composite.Ungated().Authorizer())
	require.NotNil(t, got, "resolveBuiltinBundleMCPServers must return a non-nil map even when empty")
	own, ok := got["ctxloom"]
	require.True(t, ok, "the builtin ctxloom bundle must contribute ctxloom's own MCP server; got %v", got)
	assert.Equal(t, "ctxloom", own.Command, "the bundle declares the bare name; the writers resolve it")
	assert.Equal(t, []string{"mcp", "serve"}, own.Args, "the `mcp serve` leaf is the one spelling that speaks the protocol")
	assert.Equal(t, "bundle:ctxloom+builtin:ctxloom-mcp", own.SCM)

	// Pin the contract directly: a synthetic builtin source through
	// extractMCPFromBundle produces the expected SCM tag.
	synthetic := extractMCPFromBundle(report.Reporter{}, bundles.ProjectAuthoredRead("fixture", &bundles.Bundle{
		MCP: map[string]bundles.BundleMCP{
			"synthetic": {Command: "fake"},
		},
	}), mustBuiltinRef(t, "future-bundle"), composite.Ungated().Authorizer())
	require.Contains(t, synthetic, "synthetic")
	assert.Equal(t, "bundle:ctxloom+builtin:future-bundle", synthetic["synthetic"].SCM,
		"extractMCPFromBundle prepends 'bundle:' to the source's canonical BundleIdentity, including a builtin's")
}

func TestHooksConfig_HasAny(t *testing.T) {
	assert.False(t, wire.HooksConfig{}.HasAny())
	withUnified := wire.HooksConfig{Unified: wire.UnifiedHooks{PostTool: []wire.Hook{{Command: "x"}}}}
	assert.True(t, withUnified.HasAny())
	withPlugin := wire.HooksConfig{Ext: map[string]wire.BackendHooks{
		"claude-code": {"PostToolUse": []wire.Hook{{Command: "x"}}},
	}}
	assert.True(t, withPlugin.HasAny())
}

// =============================================================================
// Save Additional Coverage
// =============================================================================

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
		appPaths: []string{tmpDir},
		lm: LMConfig{
			Defaults: RoleDefaults{Primary: "claude-code"},
			Configs: map[string]LLMConfig{
				"claude-code": {Type: "claude-code"},
			},
		},
	}

	err := cfg.saveLocked(cfg.getFS(), paths.ConfigPath(tmpDir))
	require.NoError(t, err)

	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	// Should preserve the custom field
	assert.Contains(t, string(data), "custom_field")
}

// =============================================================================
// Load Schema Validation Error
// =============================================================================

// =============================================================================
// mergeHooks Complete Coverage (SessionEnd)
// =============================================================================

// =============================================================================
// Resilient Startup Tests
// =============================================================================

// =============================================================================
// Compaction Settings Tests
// =============================================================================
// Compaction settings control how session logs are compressed for memory.

// TestPrimaryLabelModel covers what the deleted GetDefaultLLMModel accessor
// used to: the primary role's model, read through ResolveLLM directly.
//
// The accessor itself was removed by the degradation audit — it had zero
// production callers, so threading ResolveLLM's new behaviour through it would
// have been maintaining a function nothing calls. The BEHAVIOUR it asserted is
// real and still covered here.
func TestPrimaryLabelModel(t *testing.T) {
	t.Run("returns the primary label's model", func(t *testing.T) {
		cfg := &Config{lm: LMConfig{
			Configs:  map[string]LLMConfig{"big": {Type: "claude-code", Body: map[string]interface{}{"model": "sonnet"}}},
			Defaults: RoleDefaults{Primary: "big"},
		}}
		_, model := cfg.ResolveLLM(cfg.PrimaryLabel())
		assert.Equal(t, "sonnet", model)
	})

	t.Run("returns empty when the primary label has no model", func(t *testing.T) {
		cfg := &Config{}
		_, model := cfg.ResolveLLM(cfg.PrimaryLabel())
		assert.Empty(t, model)
	})
}

func TestGetCompactionLLM(t *testing.T) {
	t.Run("returns the fast role's backend", func(t *testing.T) {
		cfg := &Config{lm: LMConfig{
			Configs:  map[string]LLMConfig{"f": {Type: "mock"}},
			Defaults: RoleDefaults{Fast: "f"},
		}}
		assert.Equal(t, "mock", cfg.GetCompactionLLM())
	})

	t.Run("falls back to the primary role when no fast role", func(t *testing.T) {
		cfg := &Config{lm: LMConfig{
			Configs:  map[string]LLMConfig{"p": {Type: "claude-code"}},
			Defaults: RoleDefaults{Primary: "p"},
		}}
		assert.Equal(t, "claude-code", cfg.GetCompactionLLM())
	})

	t.Run("falls back to the bound default engine", func(t *testing.T) {
		cfg := &Config{defaultEngine: "fixture-default"}
		assert.Equal(t, "fixture-default", cfg.GetCompactionLLM())
	})
}

func TestGetCompactionModel(t *testing.T) {
	t.Run("returns the fast role's model", func(t *testing.T) {
		cfg := &Config{lm: LMConfig{
			Configs:  map[string]LLMConfig{"f": {Type: "claude-code", Body: map[string]interface{}{"model": "haiku"}}},
			Defaults: RoleDefaults{Fast: "f"},
		}}
		assert.Equal(t, "haiku", cfg.GetCompactionModel())
	})

	t.Run("empty when the fast label has no model", func(t *testing.T) {
		// No model named on the fast label → empty, so the backend supplies its own.
		cfg := &Config{lm: LMConfig{
			Configs:  map[string]LLMConfig{"f": {Type: "claude-code"}},
			Defaults: RoleDefaults{Fast: "f"},
		}}
		assert.Equal(t, "", cfg.GetCompactionModel())
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

// TestRewriteRetiredSeedParents verifies bundle-shipped profiles whose parents
// were authored in the retired top-level "@profiles/" grammar are rewritten
// in-memory to their bundle-shipped successor at seed time — seeded profiles
// never pass through the loader's document upgrade pipeline, so the seed
// post-pass owns this rewrite. Unmatched and ambiguous parents stay verbatim
// (profiles/upgrade.go owns the discovery rule).
func TestRewriteRetiredSeedParents(t *testing.T) {
	const repo = "https://github.com/ctxloom/ctxloom-default"
	loaded := map[string]*profiles.Profile{
		repo + "@bundles/ai-developer#profiles/developer": {},
		repo + "@bundles/kit#profiles/dev": {
			Parents: []string{
				repo + "@profiles/developer",    // retired, one successor → rewritten
				repo + "@profiles/go-developer", // retired, no successor → verbatim
				"local-parent",                  // local name → untouched
			},
		},
	}

	rewriteRetiredSeedParents(loaded)

	got := loaded[repo+"@bundles/kit#profiles/dev"].Parents
	assert.Equal(t, []string{
		repo + "@bundles/ai-developer#profiles/developer",
		repo + "@profiles/go-developer",
		"local-parent",
	}, got)
}

// Regression: Save round-trips the labeled-config registry, the role map, the
// config (settings) block, and the editor block. The fast role's labeled
// config carries the compression model; essence_max_chars lives under
func TestConfig_Save_PreservesLLMRolesAndEditor(t *testing.T) {
	// Real-OS-fs Load below (no WithFS): isolate HOME so the home-layer read
	// (D2/D3 layering) never reaches this developer's real ~/.ctxloom.
	testsupport.Isolate(t)
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0755))

	cfg := &Config{
		appPaths: []string{tmpDir},
		// source: SourceHome -- this represents a personal, single-file
		// config with no separate project layer (the zero value would be
		// SourceProject, and saveLocked now enforces layerscope's
		// project-scope policy whenever source is SourceProject: see its own
		// doc). editor.command/args are ScopeMachine (internal/core/config/
		// layerscope) -- legitimate in a HOME file, exactly the case this
		// represents -- but a genuine violation saveLocked now strips before
		// ever reaching a real project's committed yaml. Using
		// SourceHome here is what lets this test assert editor survives a
		// save at all, and it doubles as coverage for saveLocked's
		// skip-the-filter-when-SourceHome branch.
		source: SourceHome,
		lm: LMConfig{
			Configs: map[string]LLMConfig{
				"big":  {Type: "claude-code", Body: map[string]interface{}{"model": "opus"}},
				"fast": {Type: "mock", Body: map[string]interface{}{"model": "haiku"}},
			},
			Defaults: RoleDefaults{Primary: "big", Fast: "fast"},
		},
		settings: SettingsConfig{EssenceMaxChars: 4096},
		editor:   EditorConfig{Command: "vim", Args: []string{"-p"}},
	}
	require.NoError(t, cfg.saveLocked(cfg.getFS(), paths.ConfigPath(tmpDir)))

	// Round-trip through ParseConfig (a single-document parse, no layering)
	// rather than the layered Load: ParseConfig checks Save's own
	// serialization fidelity -- does Marshal emit every field it was given --
	// independent of any layer-scope policy (which cfg.source above already
	// keeps saveLocked from applying to this particular save).
	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	loaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, "big", loaded.ToFixture().LM.Defaults.Primary)
	assert.Equal(t, "fast", loaded.ToFixture().LM.Defaults.Fast)
	assert.Equal(t, "mock", loaded.GetCompactionLLM())
	assert.Equal(t, "haiku", loaded.GetCompactionModel())
	assert.Equal(t, 4096, loaded.GetEssenceMaxChars())
	assert.Equal(t, "vim", loaded.ToFixture().Editor.Command)
	assert.Equal(t, []string{"-p"}, loaded.ToFixture().Editor.Args)
}
