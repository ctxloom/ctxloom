package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_ReadsIsolationBase(t *testing.T) {
	for _, v := range []string{"ctxloom", "devcontainer", "ghcr.io/acme/dev:1.2"} {
		cfg, err := ParseConfig([]byte("schema_version: 7\nisolation_base: " + v + "\n"))
		require.NoError(t, err)
		assert.Equal(t, v, cfg.IsolationBase())
	}
}

func TestIsolationBase_UnsetIsEmptyAndNilSafe(t *testing.T) {
	cfg, err := ParseConfig([]byte("schema_version: 7\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.IsolationBase())
	var nilCfg *Config
	assert.Empty(t, nilCfg.IsolationBase())
}

// A bare value one or two edits from a named choice is almost certainly that
// choice misspelled: read as an image ref it would only fail at build time,
// far from the typo. Refused at load, naming the intended choice.
func TestParseConfig_RefusesANearMissIsolationBase(t *testing.T) {
	for typo, want := range map[string]string{
		"devcontaner":   IsolationBaseDevcontainer,
		"devcontainers": IsolationBaseDevcontainer,
		"ctxlom":        IsolationBaseCtxloom,
		"ctxlooom":      IsolationBaseCtxloom,
	} {
		_, err := ParseConfig([]byte("schema_version: 7\nisolation_base: " + typo + "\n"))
		require.ErrorIs(t, err, ErrIsolationBaseNearMiss, typo)
		assert.Contains(t, err.Error(), "did you mean `"+want+"`?", typo)
	}
}

// The near-miss check must not swallow real refs: a bare image name far from
// both choices, and anything shaped like a registry/tagged/digest ref, load.
func TestParseConfig_AcceptsRealImageRefs(t *testing.T) {
	for _, ref := range []string{"ubuntu", "debian", "alpine", "ctxlom:1", "acme/ctxlom", "ctxlom@sha256:abc"} {
		cfg, err := ParseConfig([]byte("schema_version: 7\nisolation_base: " + ref + "\n"))
		require.NoError(t, err, ref)
		assert.Equal(t, ref, cfg.IsolationBase())
	}
}
