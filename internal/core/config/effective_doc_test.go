package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// shippedOverlaidConfig is a project that configured no LLMs, read the way the
// reader reads it: the shipped registry fills the whole llm block.
func shippedOverlaidConfig(t *testing.T) *Config {
	t.Helper()
	cfg := NewFixture(Fixture{Version: CurrentConfigVersion, Editor: EditorConfig{Command: "vi"}})
	overlayDefaultRegistry(cfg)
	require.NotNil(t, cfg.lmDefaultOverlay, "the shipped registry must have overlaid, or these tests pin nothing")
	return cfg
}

func renderedMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := yaml.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// A shown config is the EFFECTIVE one: on a project that configured no LLMs,
// the shipped registry is what every run resolves against, so `config show`
// must print it — roles and defaults included — or it is describing a config
// nothing actually uses.
func TestShow_RendersTheShippedRegistry(t *testing.T) {
	got := renderedMap(t, shippedOverlaidConfig(t))

	llm, ok := got["llm"].(map[string]any)
	require.True(t, ok, "show must carry the llm section the shipped registry supplies: %v", got)
	configs := llm["configs"].(map[string]any)
	primary := configs["claude-code"].(map[string]any)
	assert.Equal(t, "claude-code", primary["type"])
	assert.Equal(t, "primary", primary["role"], "show renders the registry losslessly, role and all")
	assert.Equal(t, "claude-code", llm["defaults"].(map[string]any)["primary"])
}

// The authored view is what a save writes: the same document with the
// shipped registry left out.
func TestAuthored_OmitsTheShippedRegistry(t *testing.T) {
	got := renderedMap(t, shippedOverlaidConfig(t).Authored())

	assert.NotContains(t, got, "llm", "nothing the user wrote lives under llm")
	assert.Contains(t, got, "editor", "everything the user did write is still there")
}

// A save never freezes a release's defaults into a file. Every save path —
// home and project layer alike — writes the authored view, so the shipped
// entries keep tracking future releases.
func TestSave_NeverWritesTheShippedRegistry(t *testing.T) {
	for _, src := range []ConfigSource{SourceHome, SourceProject} {
		cfg := shippedOverlaidConfig(t)
		cfg.source = src
		fs := afero.NewMemMapFs()
		require.NoError(t, cfg.saveLocked(fs, "/config.yaml"))
		data, err := afero.ReadFile(fs, "/config.yaml")
		require.NoError(t, err)

		var got map[string]any
		require.NoError(t, yaml.Unmarshal(data, &got))
		assert.NotContains(t, got, "llm", "source %v: a save wrote shipped registry entries:\n%s", src, data)
	}
}

// Role is authored data, not derived state: the shipped registry carries it,
// and a user registry may too. A lossless document writes what the layer
// holds, so a role a user set survives a save and reads back as itself.
func TestSave_RoleRoundTrips(t *testing.T) {
	cfg := NewFixture(Fixture{
		Version: CurrentConfigVersion,
		LM: LMConfig{
			Configs:  map[string]LLMConfig{"quick": {Type: "codex", Role: "fast"}},
			Defaults: RoleDefaults{Fast: "quick"},
		},
	})
	cfg.source = SourceHome
	fs := afero.NewMemMapFs()
	require.NoError(t, cfg.saveLocked(fs, "/config.yaml"))
	data, err := afero.ReadFile(fs, "/config.yaml")
	require.NoError(t, err)

	back, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, "fast", back.lm.Configs["quick"].Role, "role was dropped on save:\n%s", data)
}
