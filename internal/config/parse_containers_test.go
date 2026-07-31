package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// U048-F18: ParseConfig pre-populates BOTH lm.Configs and profiles.Definitions
// with empty maps before unmarshalling, but re-guards only lm.Configs
// afterwards. A document that nulls the profile map out therefore leaves
// Definitions nil while Configs is non-nil — two containers with the same
// contract, one of them honoured.
//
// One correction to the row's reproduction, which matters because the obvious
// case does NOT reproduce it: a bare `profiles: null` is absorbed by
// Config.UnmarshalYAML's decode-into-existing-value semantics (yaml.v3 leaves
// the field alone for a null node), so the pre-population survives. The
// asymmetry surfaces one level down, at `profiles: {definitions: null}`, which
// decodes the null into the map field itself. Measured both ways below so the
// pin cannot silently stop reproducing.
//
// The guard belongs in fromDoc rather than in ParseConfig, because ParseConfig
// is not the only decoder: loadLayeredConfig decodes the merged layer into a
// Config through the same UnmarshalYAML path, so a fix at the cited function
// would have left the path a real user config actually takes still broken.

func TestParseConfig_NullProfileDefinitionsStillYieldsAUsableMap(t *testing.T) {
	t.Run("bare_null_is_absorbed_by_decode_into_existing", func(t *testing.T) {
		cfg, err := ParseConfig([]byte("version: 5\nprofiles: null\n"))
		require.NoError(t, err)
		assert.NotNil(t, cfg.profiles.Definitions)
		assert.NotNil(t, cfg.lm.Configs)
	})

	t.Run("explicit_null_definitions_is_the_reproducer", func(t *testing.T) {
		cfg, err := ParseConfig([]byte("version: 5\nprofiles:\n  definitions: null\n"))
		require.NoError(t, err)
		assert.NotNil(t, cfg.profiles.Definitions,
			"profiles.Definitions is pre-populated for the same reason lm.Configs is — downstream code assumes it is writable")
		assert.NotNil(t, cfg.lm.Configs)
	})

	t.Run("null_llm_configs_is_the_half_that_was_already_guarded", func(t *testing.T) {
		cfg, err := ParseConfig([]byte("version: 5\nllm:\n  configs: null\n"))
		require.NoError(t, err)
		assert.NotNil(t, cfg.lm.Configs)
		assert.NotNil(t, cfg.profiles.Definitions)
	})
}

// TestLoad_NullProfileDefinitionsStillYieldsAUsableMap is the path that
// matters: a real config.yaml read through Load, not ParseConfig's two
// embedded-resource callers. This is what proves the guard had to move.
func TestLoad_NullProfileDefinitionsStillYieldsAUsableMap(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"),
		[]byte("version: 5\nprofiles:\n  definitions: null\n"), 0o644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	require.NotNil(t, cfg.profiles.Definitions,
		"a user config that nulls the definitions map must still load with a writable one, exactly as the llm half does")
	assert.NotPanics(t, func() { cfg.profiles.Definitions["p"] = Profile{} },
		"the whole point of the pre-population is that the map is writable")
	assert.NotNil(t, cfg.lm.Configs)
}
