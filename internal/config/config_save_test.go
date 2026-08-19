package config

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

func ptrBool(b bool) *bool { return &b }

// TestApplyConfigSections_EditorPresence pins the setOrDelete predicate for the
// editor block. It must be emitted exactly when its presence predicate is true
// and pruned otherwise — including when only one disjunct of the predicate
// holds (editor args but no command). An `mcp` key is never emitted at all:
// native MCP config does not exist, so the block has no writer.
func TestApplyConfigSections_EditorPresence(t *testing.T) {
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

// TestConfig_Save_PrunesEmptiedEditor exercises the delete branch of
// setOrDelete end-to-end: a config file that already carries an editor block
// must lose it when saved from a config where it is empty, while unknown keys
// are preserved. This is the round-trip the Marshal table can't reach, since
// Marshal always starts from an empty map.
func TestConfig_Save_PrunesEmptiedEditor(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	seed := "" +
		"version: 3\n" +
		"editor:\n  command: vim\n" +
		"custom_unknown: keepme\n"
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(seed), 0o644))

	cfg := &Config{appPaths: []string{appDir}} // Editor left zero/empty
	cfg.SetFS(fs)
	require.NoError(t, cfg.saveLocked(fs, paths.ConfigPath(appDir)))

	data, err := afero.ReadFile(fs, paths.ConfigPath(appDir))
	require.NoError(t, err)
	got := string(data)

	assert.NotContains(t, got, "editor:", "emptied editor block must be pruned")
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

	configPath := paths.ConfigPath(tmpDir)
	fs := cfg.getFS()
	require.NoError(t, cfg.saveLocked(fs, configPath))
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "configs:",
		"the overlaid default registry must not be materialized into the user's config")

	// A user-authored change persists without dragging the registry along.
	cfg.lm.Defaults.Primary = "mine"
	require.NoError(t, cfg.saveLocked(fs, configPath))
	data, err = os.ReadFile(configPath)
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

	require.NoError(t, cfg.saveLocked(cfg.getFS(), paths.ConfigPath(tmpDir)))
	data, err := os.ReadFile(paths.ConfigPath(tmpDir))
	require.NoError(t, err)
	assert.Contains(t, string(data), "mine")
}

// TestConfig_Save_LeavesNoTempFiles pins the atomic-write contract: saveLocked
// goes through a unique temp + rename (a torn config.yaml must be impossible),
// and the temp never outlives the call. The advisory-lock acquisition and
// fail-closed-on-lock-failure behaviors this file used to pin here (via the
// since-deleted Config.Save(), which had zero production callers -- every
// real write goes through Manager.Update) are covered on the actual
// production write path by TestUpdate_HoldsFileLockAcrossReadModifyWrite and
// TestUpdate_FailsClosedWhenLockCannotBeAcquired in config_manager_test.go.
func TestConfig_Save_LeavesNoTempFiles(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &Config{
		appPaths: []string{tmpDir},
		lm:       LMConfig{Configs: map[string]LLMConfig{"mine": {Type: "claude-code"}}},
	}
	require.NoError(t, cfg.saveLocked(cfg.getFS(), paths.ConfigPath(tmpDir)))

	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"), "leftover temp file: %s", e.Name())
	}
}

// --- an unparseable config.yaml must not be truncated ------------------------

// readExistingConfig exists to preserve unknown keys across a save. When the
// file will not parse it warned and returned an EMPTY map, and saveLocked
// then atomically replaced the file with only the sections
// applyConfigSections emits — so every key ctxloom does not model, and every
// key it does but the in-memory Config happens not to carry, was destroyed.
// Reached by every `ctxloom agent add` / `mcp add` / `manage` write through
// Manager.Update.
//
// "I could not read what is there" is not "there is nothing there", and it is
// certainly not a licence to overwrite it.
func TestConfig_Save_CorruptConfig_RefusesToTruncate(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	corrupt := "version: 3\neditor:\n  command: vim\n  bad: [unclosed\ncustom_unknown: keepme\n"
	path := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, path, []byte(corrupt), 0o644))

	cfg := &Config{appPaths: []string{appDir}}
	cfg.SetFS(fs)

	err := cfg.saveLocked(fs, path)
	require.Error(t, err, "a config this process cannot parse must not be overwritten")

	after, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.Equal(t, corrupt, string(after), "the corrupt file must be left exactly as it was")
}

// The control: a config that parses fine still round-trips, unknown keys and
// all. The error channel must separate "unparseable" from "ordinary".
func TestConfig_Save_ParseableConfig_StillSaves(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	path := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, path, []byte("version: 3\ncustom_unknown: keepme\n"), 0o644))

	cfg := &Config{appPaths: []string{appDir}}
	cfg.SetFS(fs)
	require.NoError(t, cfg.saveLocked(fs, path))

	after, rerr := afero.ReadFile(fs, path)
	require.NoError(t, rerr)
	assert.Contains(t, string(after), "custom_unknown: keepme")
}

// An ABSENT config is the normal first-write shape and must still save.
func TestConfig_Save_AbsentConfig_StillSaves(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	cfg := &Config{appPaths: []string{appDir}}
	cfg.SetFS(fs)
	require.NoError(t, cfg.saveLocked(fs, paths.ConfigPath(appDir)))

	exists, eerr := afero.Exists(fs, paths.ConfigPath(appDir))
	require.NoError(t, eerr)
	assert.True(t, exists)
}
