package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// lm.Configs is pre-populated with an empty map before unmarshalling so
// downstream code may write into it, and a document is free to null it back
// out. These pin that the guard survives both decoders.
//
// The guard belongs in fromDoc rather than in ParseConfig, because ParseConfig
// is not the only decoder: loadLayeredConfig decodes the merged layer into a
// Config through the same UnmarshalYAML path, so a fix at ParseConfig alone
// would have left the path a real user config actually takes still broken.
//
// This file used to pin the same contract for profiles.Definitions as well,
// which was the half that had been missed. That container is gone with the
// inline profile arm; the guard it motivated remains, for the container that
// still exists.

func TestParseConfig_NullLLMConfigsStillYieldsAUsableMap(t *testing.T) {
	t.Run("bare_null_is_absorbed_by_decode_into_existing", func(t *testing.T) {
		cfg, err := ParseConfig([]byte("version: 5\nllm: null\n"))
		require.NoError(t, err)
		assert.NotNil(t, cfg.lm.Configs)
	})

	t.Run("explicit_null_configs", func(t *testing.T) {
		cfg, err := ParseConfig([]byte("version: 5\nllm:\n  configs: null\n"))
		require.NoError(t, err)
		require.NotNil(t, cfg.lm.Configs,
			"lm.Configs is pre-populated because downstream code assumes it is writable")
		assert.NotPanics(t, func() { cfg.lm.Configs["x"] = LLMConfig{} },
			"the whole point of the pre-population is that the map is writable")
	})
}

// TestLoad_NullLLMConfigsStillYieldsAUsableMap is the path that matters: a real
// config.yaml read through Load, not ParseConfig's two embedded-resource
// callers. This is what proves the guard had to live in fromDoc.
func TestLoad_NullLLMConfigsStillYieldsAUsableMap(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"),
		[]byte("version: 5\nllm:\n  configs: null\n"), 0o644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	require.NotNil(t, cfg.lm.Configs,
		"a user config that nulls the configs map must still load with a writable one")
	assert.NotPanics(t, func() { cfg.lm.Configs["x"] = LLMConfig{} },
		"the whole point of the pre-population is that the map is writable")
}
