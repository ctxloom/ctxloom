package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func ptrBool(b bool) *bool { return &b }

// TestApplyConfigSections_EditorAndMCPPresence pins the setOrDelete predicates
// for the editor and mcp blocks (config_save.go:134/138). Each block must be
// emitted exactly when its presence predicate is true and pruned otherwise —
// including when only one disjunct of the predicate holds (e.g. editor args but
// no command, or an mcp block carrying only auto_register_ctxloom).
func TestApplyConfigSections_EditorAndMCPPresence(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantEditor  bool
		wantMCP     bool
		wantSync    bool
		wantSrvName string // when non-empty, asserted present in the mcp block
	}{
		{
			name:       "both_empty_are_pruned",
			mutate:     func(*Config) {},
			wantEditor: false,
			wantMCP:    false,
		},
		{
			name:       "editor_command_only",
			mutate:     func(c *Config) { c.editor = EditorConfig{Command: "vim"} },
			wantEditor: true,
		},
		{
			name:       "editor_args_only", // Command empty: only the len(Args)>0 disjunct holds
			mutate:     func(c *Config) { c.editor = EditorConfig{Args: []string{"-p"}} },
			wantEditor: true,
		},
		{
			name:        "mcp_servers_only",
			mutate:      func(c *Config) { c.mcp = wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "x"}}} },
			wantMCP:     true,
			wantSrvName: "srv",
		},
		{
			name: "mcp_plugins_only",
			mutate: func(c *Config) {
				c.mcp = wire.MCPConfig{Plugins: map[string]map[string]wire.MCPServer{"claude": {"p": {Command: "x"}}}}
			},
			wantMCP: true,
		},
		{
			name:    "mcp_auto_register_only", // only the AutoRegisterCtxloom != nil disjunct holds
			mutate:  func(c *Config) { c.mcp = wire.MCPConfig{AutoRegisterCtxloom: ptrBool(false)} },
			wantMCP: true,
		},
		{
			// The sync block keys on AutoSync != nil, so a zero value with the
			// pointer set (even to false) must still be persisted.
			name:     "sync_present_when_autosync_set",
			mutate:   func(c *Config) { c.sync = SyncConfig{AutoSync: ptrBool(false)} },
			wantSync: true,
		},
		{
			name:     "sync_pruned_when_autosync_nil",
			mutate:   func(*Config) {},
			wantSync: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			tt.mutate(cfg)

			out := make(map[string]interface{})
			cfg.applyConfigSections(out)

			_, hasEditor := out["editor"]
			_, hasMCP := out["mcp"]
			_, hasSync := out["sync"]
			assert.Equal(t, tt.wantEditor, hasEditor, "editor block presence")
			assert.Equal(t, tt.wantMCP, hasMCP, "mcp block presence")
			assert.Equal(t, tt.wantSync, hasSync, "sync block presence")

			if tt.wantSrvName != "" {
				data, err := cfg.Marshal()
				require.NoError(t, err)
				assert.Contains(t, string(data), tt.wantSrvName)
			}
		})
	}
}

// TestConfig_Save_PrunesEmptiedEditorAndMCP exercises the delete branch of
// setOrDelete end-to-end: a config file that already carries editor and mcp
// blocks must lose them when saved from a config where both are empty, while
// unknown keys are preserved. This is the round-trip the Marshal table can't
// reach, since Marshal always starts from an empty map.
func TestConfig_Save_PrunesEmptiedEditorAndMCP(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	seed := "" +
		"version: 3\n" +
		"editor:\n  command: vim\n" +
		"mcp:\n  servers:\n    old:\n      command: x\n" +
		"custom_unknown: keepme\n"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(seed), 0o644))

	cfg := &Config{appPaths: []string{appDir}} // Editor + MCP left zero/empty
	cfg.SetFS(fs)
	require.NoError(t, cfg.Save())

	data, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err)
	got := string(data)

	assert.NotContains(t, got, "editor:", "emptied editor block must be pruned")
	assert.NotContains(t, got, "mcp:", "emptied mcp block must be pruned")
	assert.Contains(t, got, "custom_unknown: keepme", "unknown keys must survive a save")
}

// TestConfig_Save_DoesNotPersistEmbeddedDefaults pins the registry boundary:
// mergeDefaultConfig overlays the embedded default LLM registry as a runtime
// fallback for users who configured none. Persisting that overlay would pin
// the user to a snapshot of shipped model defaults that stops tracking future
// releases — Save must write only user-authored LM configuration.
func TestConfig_Save_DoesNotPersistEmbeddedDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{appPaths: []string{tmpDir}}
	mergeDefaultConfig(cfg)
	require.NotEmpty(t, cfg.lm.Configs, "precondition: the overlay populated the registry")

	require.NoError(t, cfg.Save())
	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "configs:",
		"the overlaid default registry must not be materialized into the user's config")

	// A user-authored change persists without dragging the registry along.
	cfg.lm.Defaults.Primary = "mine"
	require.NoError(t, cfg.Save())
	data, err = os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "primary: mine")
	assert.NotContains(t, string(data), "configs:")
}

// TestConfig_Save_UserRegistryStillPersists guards the other side: a registry
// the user actually authored round-trips through Save unchanged.
func TestConfig_Save_UserRegistryStillPersists(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{
		appPaths: []string{tmpDir},
		lm: LMConfig{Configs: map[string]LLMConfig{
			"mine": {Type: "claude-code"},
		}},
	}
	mergeDefaultConfig(cfg) // no-op for a non-empty registry

	require.NoError(t, cfg.Save())
	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "mine")
}

// TestConfig_Save_TakesAdvisoryLockWhenFSNotInjected is the regression guard
// for operations/helpers.go's loadFreshConfig fix (see its doc comment): a
// Config built the way every real Load()/LoadFresh() call produces one — fs
// left nil, injectedFS left at its zero value false — must actually contend
// for config.yaml's advisory lock, not silently skip it. Before the fix,
// every production caller reaching a Config via
// operations.loadFreshConfig(nil, ...) — every MCP-server config-mutation
// handler — pre-resolved the nil fs to a concrete OS fs before ever calling
// WithFS, which set injectedFS=true and permanently skipped this exact lock:
// exactly the CLI-vs-MCP-server concurrent-Save scenario the lock exists to
// protect (see injectedFS's doc and Save's own doc, both above).
func TestConfig_Save_TakesAdvisoryLockWhenFSNotInjected(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{appPaths: []string{tmpDir}}
	require.False(t, cfg.injectedFS, "precondition: a bare Config (as every real Load produces) is not FS-injected")

	configPath, err := cfg.GetConfigFilePath()
	require.NoError(t, err)

	unlock, err := filelock.Lock(configPath + ".lock")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- cfg.Save() }()

	select {
	case <-done:
		t.Fatal("Save() returned while the advisory lock was held externally — it never actually took the lock")
	case <-time.After(200 * time.Millisecond):
		// Still blocked on the external lock, as expected.
	}

	unlock()
	select {
	case saveErr := <-done:
		assert.NoError(t, saveErr)
	case <-time.After(2 * time.Second):
		t.Fatal("Save() never completed after the external lock was released")
	}
}

// TestConfig_Save_LeavesNoTempFiles pins the atomic-write contract: Save goes
// through a unique temp + rename (a torn config.yaml must be impossible), and
// the temp never outlives the call.
func TestConfig_Save_LeavesNoTempFiles(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{
		appPaths: []string{tmpDir},
		lm:       LMConfig{Configs: map[string]LLMConfig{"mine": {Type: "claude-code"}}},
	}
	require.NoError(t, cfg.Save())

	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"), "leftover temp file: %s", e.Name())
	}
}
