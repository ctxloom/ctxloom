package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/subagents"
)

func writeAppConfig(t *testing.T, appDir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
}

// TestConfig_ParsesSubagentsKey proves the `subagents:` config key is parsed
// into Config.Subagents — the config-key source of the subagent entity.
func TestConfig_ParsesSubagentsKey(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
subagents:
  dev:
    engine: claude-code
    profiles: [go-developer, go-style]
  finder:
    profiles: [finder]
`)
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, cfg.Subagents, 2)
	assert.Equal(t, "claude-code", cfg.Subagents["dev"].Engine)
	assert.Equal(t, []string{"go-developer", "go-style"}, cfg.Subagents["dev"].Profiles)
	assert.Empty(t, cfg.Subagents["finder"].Engine, "engine is optional")
}

// TestConfigSchema_AcceptsSubagents pins the schema to the parser: a config with
// a `subagents:` block must validate (top-level additionalProperties:false would
// otherwise reject it — exactly how `sync` once silently broke).
func TestConfigSchema_AcceptsSubagents(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err)
	yaml := `subagents:
  dev:
    engine: claude-code
    profiles: [go-developer]
  finder:
    profiles: [finder]
`
	assert.NoError(t, v.ValidateBytes([]byte(yaml)))
}

// TestLoadSubagents_MergesBothSources proves the merged view folds the config
// key AND the .ctxloom/subagents/*.yaml directory, each entry carrying its
// Source, sorted by name.
func TestLoadSubagents_MergesBothSources(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
subagents:
  dev:
    engine: claude-code
    profiles: [go-developer]
`)
	subDir := filepath.Join(appDir, "subagents")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "finder.yaml"),
		[]byte("engine: fast\nprofiles: [finder]\n"), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	subs := cfg.LoadSubagents()
	require.Len(t, subs, 2)
	assert.Equal(t, "dev", subs[0].Name)
	assert.Equal(t, subagents.SourceConfig, subs[0].Source)
	assert.Equal(t, "finder", subs[1].Name)
	assert.Equal(t, filepath.Join(subDir, "finder.yaml"), subs[1].Source)

	got, ok := cfg.Subagent("finder")
	require.True(t, ok)
	assert.Equal(t, "fast", got.Engine)
	_, ok = cfg.Subagent("absent")
	assert.False(t, ok)
}

// TestLoadSubagents_ConfigWinsOnCollision proves a name defined in BOTH sources
// resolves to the config-key definition (the directory one is shadowed).
func TestLoadSubagents_ConfigWinsOnCollision(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
subagents:
  dev:
    engine: from-config
    profiles: [config-profile]
`)
	subDir := filepath.Join(appDir, "subagents")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "dev.yaml"),
		[]byte("engine: from-dir\nprofiles: [dir-profile]\n"), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	dev, ok := cfg.Subagent("dev")
	require.True(t, ok)
	assert.Equal(t, "from-config", dev.Engine, "config-key entry wins on collision")
	assert.Equal(t, subagents.SourceConfig, dev.Source)
	assert.Len(t, cfg.LoadSubagents(), 1, "the shadowed directory entry is not a second subagent")
}

// TestConfig_SaveRoundTripsSubagents proves the config-key subagents survive a
// Save (so a programmatic write — e.g. Phase F's agent-assisted setup —
// persists), while directory subagents stay in their files.
func TestConfig_SaveRoundTripsSubagents(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, "version: 5\n")
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	cfg.Subagents = map[string]subagents.Subagent{
		"dev": {Engine: "claude-code", Profiles: []string{"go-developer"}},
	}
	require.NoError(t, cfg.Save())

	reloaded, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, reloaded.Subagents, 1)
	assert.Equal(t, "claude-code", reloaded.Subagents["dev"].Engine)
	assert.Equal(t, []string{"go-developer"}, reloaded.Subagents["dev"].Profiles)
}
