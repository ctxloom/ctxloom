package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_ReadsIsolationBase(t *testing.T) {
	for _, v := range []string{"ctxloom", "devcontainer", "ghcr.io/acme/dev:1.2"} {
		cfg, err := ParseConfig([]byte("version: 5\nisolation_base: " + v + "\n"))
		require.NoError(t, err)
		assert.Equal(t, v, cfg.IsolationBase())
	}
}

func TestIsolationBase_UnsetIsEmptyAndNilSafe(t *testing.T) {
	cfg, err := ParseConfig([]byte("version: 5\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.IsolationBase())
	var nilCfg *Config
	assert.Empty(t, nilCfg.IsolationBase())
}
