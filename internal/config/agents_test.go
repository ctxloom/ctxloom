package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func writeAppConfig(t *testing.T, appDir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
}

// TestConfig_ParsesAgentsKey proves the `agents:` config key is parsed
// into Config.Agents — the config-key source of the agent entity.
func TestConfig_ParsesAgentsKey(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
agents:
  dev:
    engine: claude-code
    profiles: [go-developer, go-style]
  finder:
    profiles: [finder]
`)
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, cfg.agents, 2)
	assert.Equal(t, "claude-code", cfg.agents["dev"].Engine)
	assert.Equal(t, []string{"go-developer", "go-style"}, cfg.agents["dev"].Profiles)
	assert.Empty(t, cfg.agents["finder"].Engine, "engine is optional")
}

// TestConfig_LegacySubagentsKey_WarnsNeverErrors pins the v0.7.0 rename
// contract: the retired `subagents:` key is NOT parsed (no compat shim —
// re-init is the upgrade path) but a config still carrying it must load with
// a schema warning naming the stray key, never a hard error (CLAUDE.md fault
// tolerance: the old bindings go inert, startup proceeds). Unknown-key
// preservation on save stays generic — an old block may survive a rewrite
// verbatim; that is deliberate, not a migration.
func TestConfig_LegacySubagentsKey_WarnsNeverErrors(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
subagents:
  dev:
    engine: claude-code
    profiles: [go-developer]
`)
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err, "a legacy key must never block startup")
	assert.Empty(t, cfg.agents, "the retired key is inert, not migrated")
	assert.Empty(t, cfg.LoadAgents())
	warned := false
	for _, w := range cfg.warnings {
		if strings.Contains(w.Text, "subagents") {
			warned = true
			break
		}
	}
	assert.True(t, warned, "schema validation should warn about the stray subagents key so the rename is diagnosable; warnings: %v", cfg.warnings)
}

// TestConfigSchema_AcceptsAgents pins the schema to the parser: a config with
// an `agents:` block must validate (top-level additionalProperties:false would
// otherwise reject it — exactly how `sync` once silently broke).
func TestConfigSchema_AcceptsAgents(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err)
	yaml := `agents:
  dev:
    engine: claude-code
    profiles: [go-developer]
  finder:
    profiles: [finder]
`
	assert.NoError(t, v.ValidateBytes([]byte(yaml)))
}

// TestLoadAgents_MergesBothSources proves the merged view folds the config
// key AND the .ctxloom/agents/*.yaml directory, each entry carrying its
// Source, sorted by name.
func TestLoadAgents_MergesBothSources(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
agents:
  dev:
    engine: claude-code
    profiles: [go-developer]
`)
	subDir := filepath.Join(appDir, "agents")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "finder.yaml"),
		[]byte("engine: fast\nprofiles: [finder]\n"), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	subs := cfg.LoadAgents()
	require.Len(t, subs, 2)
	assert.Equal(t, "dev", subs[0].Name)
	assert.Equal(t, agents.SourceConfig, subs[0].Source)
	assert.Equal(t, "finder", subs[1].Name)
	assert.Equal(t, filepath.Join(subDir, "finder.yaml"), subs[1].Source)

	got, ok := cfg.Agent("finder")
	require.True(t, ok)
	assert.Equal(t, "fast", got.Engine)
	_, ok = cfg.Agent("absent")
	assert.False(t, ok)
}

// TestLoadAgents_ConfigWinsOnCollision proves a name defined in BOTH sources
// resolves to the config-key definition (the directory one is shadowed).
func TestLoadAgents_ConfigWinsOnCollision(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
agents:
  dev:
    engine: from-config
    profiles: [config-profile]
`)
	subDir := filepath.Join(appDir, "agents")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "dev.yaml"),
		[]byte("engine: from-dir\nprofiles: [dir-profile]\n"), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	dev, ok := cfg.Agent("dev")
	require.True(t, ok)
	assert.Equal(t, "from-config", dev.Engine, "config-key entry wins on collision")
	assert.Equal(t, agents.SourceConfig, dev.Source)
	assert.Len(t, cfg.LoadAgents(), 1, "the shadowed directory entry is not a second agent")
}

// TestConfig_SaveRoundTripsAgents proves the config-key agents survive a
// Save (so a programmatic write — e.g. Phase F's agent-assisted setup —
// persists), while directory agents stay in their files.
func TestConfig_SaveRoundTripsAgents(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, "version: 5\n")
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	cfg.agents = map[string]agents.Agent{
		"dev": {Engine: "claude-code", Profiles: []string{"go-developer"}},
	}
	require.NoError(t, cfg.Save())

	reloaded, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, reloaded.agents, 1)
	assert.Equal(t, "claude-code", reloaded.agents["dev"].Engine)
	assert.Equal(t, []string{"go-developer"}, reloaded.agents["dev"].Profiles)
}
