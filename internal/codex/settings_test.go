package codex

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// codexConfigPath is the config.toml a STATIC write for projectDir lands at.
// Resolved through the writer itself rather than re-spelled, so these tests
// follow the engine-home policy's single owner (internal/codex.ProjectHome) the
// same way production does — a test carrying its own copy of the path is
// exactly how the run-path/static-path split went unnoticed.
func codexConfigPath(projectDir string) string {
	return (&CodexHookWriter{}).SettingsPath(projectDir)
}

func readConfig(t *testing.T, fs afero.Fs, path string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, toml.Unmarshal(data, &cfg))
	return cfg
}

// TestWriteSettings_CompanionHookIdempotent pins re-apply behavior for hooks
// whose executable is a companion binary (no ctxloom token, no durable
// marker): the exact command must be deduplicated, while a user variant of the
// same binary with different arguments survives.
func TestWriteSettings_CompanionHookIdempotent(t *testing.T) {
	fs := afero.NewMemMapFs()
	// User's own ltk registration (different args) predates ctxloom's.
	existing := "[hooks]\n[[hooks.PreToolUse]]\nmatcher = 'Bash'\n[[hooks.PreToolUse.hooks]]\ncommand = 'ltk evaluate --config .ltk/config.yaml'\ntype = 'command'\n"
	require.NoError(t, afero.WriteFile(fs, codexConfigPath("/proj"), []byte(existing), 0644))

	w := NewWriter(agent.SettingsOptions{FS: fs})
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ltk evaluate", Matcher: "Bash", SCM: "bundle:builtin:ltk"}},
		},
	}

	countLtk := func() (exact, variant int) {
		cfg := readConfig(t, fs, codexConfigPath("/proj"))
		for _, g := range asSlice(asMap(cfg["hooks"])["PreToolUse"]) {
			for _, e := range asSlice(asMap(g)["hooks"]) {
				switch asMap(e)["command"] {
				case "ltk evaluate":
					exact++
				case "ltk evaluate --config .ltk/config.yaml":
					variant++
				}
			}
		}
		return
	}

	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	exact, variant := countLtk()
	assert.Equal(t, 1, exact, "companion hook must not duplicate across re-applies")
	assert.Equal(t, 1, variant, "user's own variant of the same binary must survive")
}

// TestWriteSettings_SameCommandDistinctMatchersCoexist guards codex-code-01-006:
// one command routed to the same Codex event under two different matchers (an
// all-tools PreToolUse from unified PreTool plus a Bash-scoped one from unified
// PreShell) must keep BOTH groups across re-applies. Dedup keys on the
// (command, matcher) pair, so a matcher-blind purge can no longer silently drop
// the broader all-tools coverage. With the old command-only match the no-matcher
// group is removed when PreShell re-adds the same command, leaving only Bash.
func TestWriteSettings_SameCommandDistinctMatchersCoexist(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := NewWriter(agent.SettingsOptions{FS: fs})
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool:  []wire.Hook{{Command: "guard run"}}, // -> PreToolUse, no matcher
			PreShell: []wire.Hook{{Command: "guard run"}}, // -> PreToolUse, matcher "Bash"
		},
	}

	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	byMatcher := map[string]int{}
	cfg := readConfig(t, fs, codexConfigPath("/proj"))
	for _, g := range asSlice(asMap(cfg["hooks"])["PreToolUse"]) {
		gm := asMap(g)
		m, _ := gm["matcher"].(string)
		for _, e := range asSlice(gm["hooks"]) {
			if asMap(e)["command"] == "guard run" {
				byMatcher[m]++
			}
		}
	}
	assert.Equal(t, 1, byMatcher[""], "all-tools PreToolUse coverage preserved (no matcher), not duplicated")
	assert.Equal(t, 1, byMatcher["Bash"], "Bash-scoped PreToolUse coverage preserved, not duplicated")
}

// TestWriteSettings_HooksAndMCP verifies the writer emits codex's
// [[hooks.EVENT]] groups and [mcp_servers] table, auto-registers ctxloom, and
// preserves unrelated user keys.
func TestWriteSettings_HooksAndMCP(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Pre-existing user config the writer must preserve.
	require.NoError(t, afero.WriteFile(fs, codexConfigPath("/proj"), []byte("model = \"o3\"\n"), 0644))

	w := NewWriter(agent.SettingsOptions{FS: fs})
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
			PreTool:      []wire.Hook{{Command: "ctxloom hook stamp", Matcher: "Bash"}},
		},
	}
	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"context7": {Command: "npx", Args: []string{"-y", "@upstash/context7-mcp"}},
	}}

	require.NoError(t, w.WriteSettings(hooks, mcp, nil, "/proj"))

	cfg := readConfig(t, fs, codexConfigPath("/proj"))
	assert.Equal(t, "o3", cfg["model"], "user key preserved")

	hookTbl := asMap(cfg["hooks"])
	require.NotNil(t, hookTbl)
	assert.NotEmpty(t, asSlice(hookTbl["SessionStart"]), "SessionStart group written")
	assert.NotEmpty(t, asSlice(hookTbl["PreToolUse"]), "PreTool maps to PreToolUse")

	servers := asMap(cfg["mcp_servers"])
	require.NotNil(t, servers)
	assert.Contains(t, servers, "context7", "config MCP server written")
	assert.Contains(t, servers, agent.MCPServerName, "ctxloom server auto-registered")

	status, err := w.Status("/proj")
	require.NoError(t, err)
	assert.True(t, status.HooksPresent)
	assert.True(t, status.MCPPresent)
}

// TestWriteSettings_MCPCommandOverride pins dire-five's fix at codex's own
// writer: a zero-value writer (MCPCommandOverride unset — every cell but an
// isolated container) emits EXACTLY agent.CtxloomCommand()'s host self-exec-
// absolute path into [mcp_servers.ctxloom]; a writer with the override set
// (the container-cell path, surfaces.go's configSurface) emits the override
// instead.
func TestWriteSettings_MCPCommandOverride(t *testing.T) {
	command := func(t *testing.T, fs afero.Fs) string {
		t.Helper()
		cfg := readConfig(t, fs, codexConfigPath("/proj"))
		servers := asMap(cfg["mcp_servers"])
		require.NotNil(t, servers)
		entry := asMap(servers[agent.MCPServerName])
		cmd, _ := entry["command"].(string)
		return cmd
	}

	t.Run("host-unchanged: no override writes CtxloomCommand()", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		w := &CodexHookWriter{FS: fs}
		require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, nil, nil, "/proj"))
		assert.Equal(t, agent.CtxloomCommand(), command(t, fs))
	})

	t.Run("container cell: override wins", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		const containerBin = "/usr/local/bin/ctxloom"
		w := &CodexHookWriter{FS: fs, MCPCommandOverride: containerBin}
		require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, nil, nil, "/proj"))
		assert.Equal(t, containerBin, command(t, fs))
	})
}

// TestWriteSettings_MCPServerEnvPreserved verifies env vars on MCP servers
// from config, bundles, and backend passthrough all reach [mcp_servers.NAME]
// as an env table, matching what MCPRegistrar.Install writes.
func TestWriteSettings_MCPServerEnvPreserved(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := NewWriter(agent.SettingsOptions{FS: fs})

	mcp := &wire.MCPConfig{
		Servers: map[string]wire.MCPServer{
			"config-server": {
				Command: "config-cmd",
				Args:    []string{"--flag"},
				Env:     map[string]string{"CONFIG_TOKEN": "abc123"},
			},
		},
		Plugins: map[string]map[string]wire.MCPServer{
			"codex": {
				"plugin-server": {Command: "plugin-cmd", Env: map[string]string{"PLUGIN_KEY": "xyz"}},
			},
		},
	}
	bundleMCP := map[string]wire.MCPServer{
		"bundle-server": {Command: "bundle-cmd", Env: map[string]string{"BUNDLE_VAR": "value"}},
	}

	require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, mcp, bundleMCP, "/proj"))

	cfg := readConfig(t, fs, codexConfigPath("/proj"))
	servers := asMap(cfg["mcp_servers"])
	require.NotNil(t, servers)

	wantEnv := map[string]map[string]any{
		"config-server": {"CONFIG_TOKEN": "abc123"},
		"plugin-server": {"PLUGIN_KEY": "xyz"},
		"bundle-server": {"BUNDLE_VAR": "value"},
	}
	for name, want := range wantEnv {
		entry := asMap(servers[name])
		require.NotNil(t, entry, "%s entry written", name)
		assert.Equal(t, want, asMap(entry["env"]), "%s env preserved", name)
	}

	// The auto-registered ctxloom server carries no env.
	ctxloomEntry := asMap(servers[agent.MCPServerName])
	require.NotNil(t, ctxloomEntry)
	assert.NotContains(t, ctxloomEntry, "env", "ctxloom auto-server has no env key")
}

// TestRemoveSettings strips ctxloom-managed hooks + MCP but keeps user content.
func TestRemoveSettings(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := NewWriter(agent.SettingsOptions{FS: fs})

	hooks := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
	}}
	require.NoError(t, w.WriteSettings(hooks, nil, nil, "/proj"))

	// A user-authored hook the writer must not touch.
	cfg := readConfig(t, fs, codexConfigPath("/proj"))
	hookTbl := asMap(cfg["hooks"])
	hookTbl["Stop"] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/usr/bin/mine"}}}}
	var buf []byte
	buf, err := toml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, codexConfigPath("/proj"), buf, 0644))

	require.NoError(t, w.RemoveSettings("/proj"))

	got := readConfig(t, fs, codexConfigPath("/proj"))
	gotHooks := asMap(got["hooks"])
	assert.NotContains(t, gotHooks, "SessionStart", "ctxloom hook removed")
	assert.Contains(t, gotHooks, "Stop", "user hook preserved")
}
